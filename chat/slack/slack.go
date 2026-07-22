// Package slack implements a chat.Conn for Slack using Socket Mode.
//
// The backend is selected with slack:// and reads its bot and app tokens from
// the configured credential store. Messages must begin with Slack's native <@USERID>
// recipient mention, which is removed before the command is delivered.
//
// Each root message starts a conversation. Replies in the same Slack thread
// share its conversation ID, and outbound messages are posted to that thread.
// Optional chat.Message choices are rendered as one-shot Block Kit buttons.
package slack

import (
	"context"
	"fmt"
	"net/url"
	"sync"
	"sync/atomic"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"

	"github.com/hangxie/chatops/chat"
	"github.com/hangxie/chatops/cred"
)

// Scheme is the URL scheme this backend serves in a chat.Registry.
const Scheme = "slack"

type socketClient interface {
	Events() <-chan socketmode.Event
	RunContext(context.Context) error
	Ack(context.Context, *socketmode.Request) error
}

type messageAPI interface {
	BotUserID(ctx context.Context) (string, error)
	PostMessage(ctx context.Context, channel, thread string, msg chat.Message) (string, error)
	ClearChoices(ctx context.Context, channel, timestamp, text string) error
}

type clientFactory func(botToken, appToken string) (socketClient, messageAPI)

type socketAdapter struct{ client *socketmode.Client }

func (s *socketAdapter) Events() <-chan socketmode.Event { return s.client.Events }
func (s *socketAdapter) RunContext(ctx context.Context) error {
	return s.client.RunContext(ctx)
}

func (s *socketAdapter) Ack(ctx context.Context, req *socketmode.Request) error {
	return s.client.AckCtx(ctx, req.EnvelopeID, nil)
}

type webAPI struct{ client *slackapi.Client }

func (a *webAPI) BotUserID(ctx context.Context) (string, error) {
	identity, err := a.client.AuthTestContext(ctx)
	if err != nil {
		return "", err
	}
	return identity.UserID, nil
}

func (a *webAPI) PostMessage(ctx context.Context, channel, thread string, msg chat.Message) (string, error) {
	options := []slackapi.MsgOption{slackapi.MsgOptionText(msg.Text, false), slackapi.MsgOptionTS(thread)}
	if len(msg.Choices) > 0 {
		blocks, err := messageBlocks(msg)
		if err != nil {
			return "", err
		}
		options = append(options, slackapi.MsgOptionBlocks(blocks...))
	}
	_, timestamp, err := a.client.PostMessageContext(ctx, channel, options...)
	return timestamp, err
}

func (a *webAPI) ClearChoices(ctx context.Context, channel, timestamp, text string) error {
	_, _, _, err := a.client.UpdateMessageContext(ctx, channel, timestamp,
		slackapi.MsgOptionText(text, false), slackapi.MsgOptionBlocks())
	return err
}

// Conn is a chat.Conn backed by Slack Socket Mode and the Web API.
type Conn struct {
	socket socketClient
	api    messageAPI
	botID  string

	ctx    context.Context
	cancel context.CancelFunc
	msgs   chan chat.Message
	done   chan struct{}

	closed    atomic.Bool
	closeOnce sync.Once
	readErr   error

	routes  *routeCache
	prompts *routeCache

	startup     chan error
	startupOnce sync.Once
	runResult   chan error
}

// Opener is the chat.OpenerFunc for this backend. slack:// takes no host,
// path, query parameters, or fragment; credentials come from creds.
func Opener(ctx context.Context, u *url.URL, creds cred.Store) (chat.Conn, error) {
	if u.Host != "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, fmt.Errorf("slack: URL %q takes no configuration", u.String())
	}
	return Open(ctx, creds)
}

// Open connects to Slack using the predefined Slack credentials from creds.
func Open(ctx context.Context, creds cred.Store) (*Conn, error) {
	return open(ctx, creds, defaultClients)
}

func defaultClients(botToken, appToken string) (socketClient, messageAPI) {
	client := slackapi.New(botToken, slackapi.OptionAppLevelToken(appToken))
	return &socketAdapter{client: socketmode.New(client)}, &webAPI{client: client}
}

func open(ctx context.Context, creds cred.Store, clients clientFactory) (*Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("slack: %w", err)
	}
	botToken, err := cred.Require(ctx, creds, cred.SlackBotToken)
	if err != nil {
		return nil, fmt.Errorf("slack: %w", err)
	}
	appToken, err := cred.Require(ctx, creds, cred.SlackAppToken)
	if err != nil {
		return nil, fmt.Errorf("slack: %w", err)
	}

	socket, api := clients(botToken, appToken)
	botID, err := api.BotUserID(ctx)
	if err != nil {
		return nil, fmt.Errorf("slack: identify bot: %w", err)
	}
	if botID == "" {
		return nil, fmt.Errorf("slack: identify bot: empty user ID")
	}
	conn := newConn(socket, api, botID)
	select {
	case err := <-conn.startup:
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		return conn, nil
	case <-ctx.Done():
		_ = conn.Close()
		return nil, fmt.Errorf("slack: connect: %w", ctx.Err())
	}
}

func newConn(socket socketClient, api messageAPI, botID string) *Conn {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Conn{
		socket:    socket,
		api:       api,
		botID:     botID,
		ctx:       ctx,
		cancel:    cancel,
		msgs:      make(chan chat.Message, 50),
		done:      make(chan struct{}),
		routes:    newRouteCache(defaultConversationTTL, defaultMaxConversationRoutes),
		prompts:   newRouteCache(defaultPromptTTL, defaultMaxPrompts),
		startup:   make(chan error, 1),
		runResult: make(chan error, 1),
	}
	go func() { c.runResult <- c.socket.RunContext(c.ctx) }()
	go c.readLoop()
	return c
}

func (c *Conn) readLoop() {
	defer close(c.done)
	defer close(c.msgs)
	for {
		select {
		case <-c.ctx.Done():
			c.readErr = fmt.Errorf("slack: %w", chat.ErrClosed)
			c.signalStartup(c.readErr)
			return
		case err := <-c.runResult:
			if c.closed.Load() {
				c.readErr = fmt.Errorf("slack: %w", chat.ErrClosed)
			} else {
				c.readErr = fmt.Errorf("slack: connection lost: %w", err)
			}
			c.signalStartup(c.readErr)
			return
		case event := <-c.socket.Events():
			if event.Type == socketmode.EventTypeConnected {
				c.signalStartup(nil)
			}
			if event.Request != nil {
				if err := c.socket.Ack(c.ctx, event.Request); err != nil {
					c.readErr = fmt.Errorf("slack: acknowledge event: %w", err)
					c.signalStartup(c.readErr)
					return
				}
			}
			msg, ok, err := c.messageFromSocketEvent(event)
			if err != nil {
				c.readErr = err
				return
			}
			if !ok {
				continue
			}
			select {
			case c.msgs <- msg:
			case <-c.ctx.Done():
				c.readErr = fmt.Errorf("slack: %w", chat.ErrClosed)
				return
			}
		}
	}
}

func (c *Conn) messageFromSocketEvent(event socketmode.Event) (chat.Message, bool, error) {
	msg, route, ok := messageFromEvent(event, c.botID)
	if ok {
		c.routes.Remember(msg.ConversationID, route)
		return msg, true, nil
	}
	choice, ok := choiceFromEvent(event)
	if !ok {
		return chat.Message{}, false, nil
	}
	route, ok = c.prompts.TakeChoice(promptID(choice.channel, choice.messageTimestamp), choice.message.Text)
	if !ok || route.channel != choice.channel {
		return chat.Message{}, false, nil
	}
	choice.message.ConversationID = conversationID(route.channel, route.thread)
	if err := c.api.ClearChoices(c.ctx, choice.channel, choice.messageTimestamp, choice.displayText); err != nil {
		return chat.Message{}, false, fmt.Errorf("slack: clear choices: %w", err)
	}
	return choice.message, true, nil
}

func (c *Conn) signalStartup(err error) {
	c.startupOnce.Do(func() { c.startup <- err })
}

// Receive returns the next human Slack message or app mention.
func (c *Conn) Receive(ctx context.Context) (chat.Message, error) {
	if c.closed.Load() {
		return chat.Message{}, fmt.Errorf("slack: %w", chat.ErrClosed)
	}
	select {
	case <-ctx.Done():
		return chat.Message{}, fmt.Errorf("slack: %w", ctx.Err())
	case msg, ok := <-c.msgs:
		if c.closed.Load() {
			return chat.Message{}, fmt.Errorf("slack: %w", chat.ErrClosed)
		}
		if !ok {
			return chat.Message{}, c.readErr
		}
		return msg, nil
	}
}

// Send posts msg.Text and any interactive choices in the mapped Slack thread.
func (c *Conn) Send(ctx context.Context, msg chat.Message) error {
	if c.closed.Load() {
		return fmt.Errorf("slack: %w", chat.ErrClosed)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("slack: %w", err)
	}
	target, ok := c.routes.Lookup(msg.ConversationID)
	if !ok {
		return fmt.Errorf("slack: conversation %q: %w", msg.ConversationID, chat.ErrUnknownConversation)
	}
	if len(msg.Choices) > 0 {
		if _, err := messageBlocks(msg); err != nil {
			return fmt.Errorf("slack: send: %w", err)
		}
	}
	timestamp, err := c.api.PostMessage(ctx, target.channel, target.thread, msg)
	if err != nil {
		return fmt.Errorf("slack: send: %w", err)
	}
	if len(msg.Choices) > 0 {
		if timestamp == "" {
			return fmt.Errorf("slack: send choices: empty message timestamp")
		}
		values := make([]string, len(msg.Choices))
		for i, choice := range msg.Choices {
			values[i] = choice.Value
		}
		c.prompts.RememberChoices(promptID(target.channel, timestamp), target, values)
	}
	return nil
}

func promptID(channel, timestamp string) string { return channel + "\x00" + timestamp }

func messageBlocks(msg chat.Message) ([]slackapi.Block, error) {
	elements := make([]slackapi.BlockElement, 0, len(msg.Choices))
	for i, choice := range msg.Choices {
		if choice.Label == "" || choice.Value == "" {
			return nil, fmt.Errorf("invalid choice %q", choice.Value)
		}
		button := slackapi.NewButtonBlockElement(choiceActionID(i), choice.Value,
			slackapi.NewTextBlockObject(slackapi.PlainTextType, choice.Label, true, false))
		switch choice.Value {
		case "yes":
			button.WithStyle(slackapi.StylePrimary)
		}
		elements = append(elements, button)
	}
	return []slackapi.Block{
		slackapi.NewSectionBlock(slackapi.NewTextBlockObject(slackapi.MarkdownType, msg.Text, false, false), nil, nil),
		slackapi.NewActionBlock("chatops.choices", elements...),
	}, nil
}

// Close terminates the Socket Mode connection and unblocks Receive.
func (c *Conn) Close() error {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		c.cancel()
		<-c.done
	})
	return nil
}
