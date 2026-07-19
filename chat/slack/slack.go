// Package slack implements a chat.Conn for Slack using Socket Mode.
//
// The backend is selected with slack:// and reads tokens from SLACK_BOT_TOKEN
// and SLACK_APP_TOKEN. Messages must begin with Slack's native <@USERID>
// recipient mention, which is removed before the command is delivered.
//
// Each root message starts a conversation. Replies in the same Slack thread
// share its conversation ID, and outbound messages are posted to that thread.
package slack

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync"
	"sync/atomic"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"

	"github.com/hangxie/chatops/chat"
)

const (
	// Scheme is the URL scheme this backend serves in a chat.Registry.
	Scheme = "slack"
	// BotTokenEnv names the environment variable holding the bot OAuth token.
	BotTokenEnv = "SLACK_BOT_TOKEN"
	// AppTokenEnv names the environment variable holding the Socket Mode app token.
	AppTokenEnv = "SLACK_APP_TOKEN"
)

type socketClient interface {
	Events() <-chan socketmode.Event
	RunContext(context.Context) error
	Ack(context.Context, *socketmode.Request) error
}

type messageAPI interface {
	BotUserID(ctx context.Context) (string, error)
	PostMessage(ctx context.Context, channel, thread, text string) error
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

func (a *webAPI) PostMessage(ctx context.Context, channel, thread, text string) error {
	_, _, err := a.client.PostMessageContext(
		ctx,
		channel,
		slackapi.MsgOptionText(text, false),
		slackapi.MsgOptionTS(thread),
	)
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

	routes *routeCache

	startup     chan error
	startupOnce sync.Once
	runResult   chan error
}

// Opener is the chat.OpenerFunc for this backend. slack:// takes no host,
// path, query parameters, or fragment; credentials come from the environment.
func Opener(ctx context.Context, u *url.URL) (chat.Conn, error) {
	if u.Host != "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, fmt.Errorf("slack: URL %q takes no configuration", u.String())
	}
	return Open(ctx)
}

// Open connects to Slack using credentials from BotTokenEnv and AppTokenEnv.
func Open(ctx context.Context) (*Conn, error) {
	return open(ctx, os.Getenv, defaultClients)
}

func defaultClients(botToken, appToken string) (socketClient, messageAPI) {
	client := slackapi.New(botToken, slackapi.OptionAppLevelToken(appToken))
	return &socketAdapter{client: socketmode.New(client)}, &webAPI{client: client}
}

func open(ctx context.Context, getenv func(string) string, clients clientFactory) (*Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("slack: %w", err)
	}
	botToken := getenv(BotTokenEnv)
	if botToken == "" {
		return nil, fmt.Errorf("slack: %s is not set", BotTokenEnv)
	}
	appToken := getenv(AppTokenEnv)
	if appToken == "" {
		return nil, fmt.Errorf("slack: %s is not set", AppTokenEnv)
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
			msg, route, ok := messageFromEvent(event, c.botID)
			if !ok {
				continue
			}
			c.routes.Remember(msg.ConversationID, route)
			select {
			case c.msgs <- msg:
			case <-c.ctx.Done():
				c.readErr = fmt.Errorf("slack: %w", chat.ErrClosed)
				return
			}
		}
	}
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

// Send posts msg.Text as a reply in the mapped Slack thread.
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
	if err := c.api.PostMessage(ctx, target.channel, target.thread, msg.Text); err != nil {
		return fmt.Errorf("slack: send: %w", err)
	}
	return nil
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
