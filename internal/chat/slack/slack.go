// Package slack receives Slack messages over Socket Mode and replies in their threads or channels.
package slack

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
	"unicode/utf8"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/hangxie/chatops/internal/config"
	"github.com/hangxie/chatops/internal/conversation"
)

const (
	dedupeTTL   = time.Hour
	dedupeLimit = 10000
	// maxReplyRunes keeps escaped text (up to 5x longer) under Slack's 40,000-character limit;
	// block replies are bounded by Block Kit limits instead.
	maxReplyRunes = 7000
	// maxFooterRunes bounds the footer in a plain-text reply, so the answer is cut first.
	maxFooterRunes = 2000
	truncatedNote  = "\n[reply truncated]"
	busyReply      = "I'm handling too many requests right now; please try again shortly."
	busyTimeout    = 10 * time.Second
	// maxBusyReplies bounds busy replies in flight; more are skipped rather than queued.
	maxBusyReplies = 4
)

type socketClient interface {
	Events() <-chan socketmode.Event
	RunContext(ctx context.Context) error
	Ack(ctx context.Context, req *socketmode.Request) error
}

type socket struct{ client *socketmode.Client }

func (s socket) Events() <-chan socketmode.Event      { return s.client.Events }
func (s socket) RunContext(ctx context.Context) error { return s.client.RunContext(ctx) }
func (s socket) Ack(ctx context.Context, req *socketmode.Request) error {
	return s.client.AckCtx(ctx, req.EnvelopeID, nil)
}

// Adapter turns Slack events into conversation messages.
type Adapter struct {
	socket         socketClient
	api            *slackapi.Client
	logger         *slog.Logger
	now            func() time.Time
	seen           *seenSet
	botID          string
	replyInChannel bool

	busySlots chan struct{}
	busy      sync.WaitGroup
}

// New builds an adapter from the bot and app-level tokens named in cfg.
func New(cfg config.ChatBackend, logger *slog.Logger) (*Adapter, error) {
	botToken, err := config.Secret(cfg.BotTokenEnv)
	if err != nil {
		return nil, fmt.Errorf("slack bot token: %w", err)
	}
	appToken, err := config.Secret(cfg.AppTokenEnv)
	if err != nil {
		return nil, fmt.Errorf("slack app token: %w", err)
	}
	api := slackapi.New(botToken, slackapi.OptionAppLevelToken(appToken))
	adapter := newAdapter(socket{client: socketmode.New(api)}, api, logger)
	adapter.replyInChannel = cfg.ReplyInChannel
	return adapter, nil
}

func newAdapter(socket socketClient, api *slackapi.Client, logger *slog.Logger) *Adapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Adapter{
		socket:    socket,
		api:       api,
		logger:    logger,
		now:       time.Now,
		seen:      newSeenSet(dedupeTTL, dedupeLimit),
		busySlots: make(chan struct{}, maxBusyReplies),
	}
}

// Run hands each new message to submit until ctx ends; Socket Mode reconnects on its own.
func (a *Adapter) Run(ctx context.Context, submit func(conversation.Message) error) error {
	auth, err := a.api.AuthTestContext(ctx)
	if err != nil {
		return fmt.Errorf("slack: identify bot: %w", err)
	}
	a.botID = auth.UserID

	ctx, cancel := context.WithCancel(ctx)
	runErr := make(chan error, 1)
	go func() { runErr <- a.socket.RunContext(ctx) }()
	err = a.consume(ctx, runErr, submit)
	cancel()
	a.busy.Wait()
	return err
}

// consume handles events until ctx ends or Socket Mode stops; it never waits on the Web API.
func (a *Adapter) consume(ctx context.Context, runErr <-chan error, submit func(conversation.Message) error) error {
	for {
		select {
		case <-ctx.Done():
			<-runErr
			return nil
		case err := <-runErr:
			switch {
			case ctx.Err() != nil:
				return nil
			case err == nil:
				return errors.New("slack: socket mode stopped unexpectedly")
			}
			return fmt.Errorf("slack: socket mode: %w", err)
		case event, ok := <-a.socket.Events():
			if !ok {
				return errors.New("slack: socket mode event stream closed")
			}
			a.handle(ctx, event, submit)
		}
	}
}

func (a *Adapter) handle(ctx context.Context, event socketmode.Event, submit func(conversation.Message) error) {
	if event.Request != nil {
		if err := a.socket.Ack(ctx, event.Request); err != nil {
			a.logger.Warn("slack ack failed", "error", err)
		}
	}
	switch event.Type {
	case socketmode.EventTypeConnected:
		a.logger.Info("slack connected")
	case socketmode.EventTypeConnectionError, socketmode.EventTypeInvalidAuth:
		a.logger.Warn("slack connection problem", "type", event.Type)
	case socketmode.EventTypeEventsAPI:
		outer, ok := event.Data.(slackevents.EventsAPIEvent)
		if !ok {
			return
		}
		in, ok := parseEvent(outer, a.botID)
		if !ok || a.seen.has(in.id(), a.now()) {
			return
		}
		a.submit(ctx, in, submit)
	}
}

func (a *Adapter) submit(ctx context.Context, in inbound, submit func(conversation.Message) error) {
	log := a.logger.With("conversation", in.conversation(), "user", in.user)
	thread := in.thread
	if a.replyInChannel && thread == in.ts {
		thread = ""
	}
	reply := a.replier(in.channel, thread)
	err := submit(conversation.Message{Conversation: in.conversation(), User: in.user, Text: in.text, Reply: reply})
	switch {
	case err == nil:
		a.seen.add(in.id(), a.now())
		log.Info("slack message accepted")
	case errors.Is(err, conversation.ErrBusy):
		log.Warn("slack message refused: busy")
		a.replyBusy(ctx, log, reply)
	default:
		log.Warn("slack message dropped", "error", err)
	}
}

// replyBusy posts the busy reply in the background, skipping it when maxBusyReplies are in flight.
func (a *Adapter) replyBusy(ctx context.Context, log *slog.Logger, reply func(context.Context, conversation.Reply) error) {
	select {
	case a.busySlots <- struct{}{}:
	default:
		log.Warn("slack busy reply skipped: too many in flight")
		return
	}
	a.busy.Go(func() {
		defer func() { <-a.busySlots }()
		ctx, cancel := context.WithTimeout(ctx, busyTimeout)
		defer cancel()
		if err := reply(ctx, conversation.Reply{Text: busyReply}); err != nil {
			log.Warn("slack busy reply failed", "error", err)
		}
	})
}

// replier posts escaped text as Slack mrkdwn without link previews, so model output cannot ping, link, or unfurl.
// A reply with a Markdown table or a footer is sent as blocks; Slack shows the text only in notifications.
func (a *Adapter) replier(channel, thread string) func(context.Context, conversation.Reply) error {
	return func(ctx context.Context, reply conversation.Reply) error {
		opts := []slackapi.MsgOption{
			slackapi.MsgOptionDisableLinkUnfurl(),
			slackapi.MsgOptionDisableMediaUnfurl(),
		}
		if thread != "" {
			opts = append(opts, slackapi.MsgOptionTS(thread))
		}
		if blocks := toBlocks(reply); blocks != nil {
			blockOpts := append(opts[:len(opts):len(opts)], slackapi.MsgOptionText(limitReply(toMrkdwn(reply.Text), maxReplyRunes), true), slackapi.MsgOptionBlocks(blocks...))
			_, _, err := a.api.PostMessageContext(ctx, channel, blockOpts...)
			var slackErr slackapi.SlackErrorResponse
			if !errors.As(err, &slackErr) || slackErr.Err != "invalid_blocks" {
				return err
			}
			a.logger.Warn("slack rejected reply blocks; replying as text", "error", err)
		}
		opts = append(opts, slackapi.MsgOptionText(plainText(reply), true))
		_, _, err := a.api.PostMessageContext(ctx, channel, opts...)
		return err
	}
}

// plainText fits a reply into maxReplyRunes, cutting the answer rather than the footer.
func plainText(reply conversation.Reply) string {
	if reply.Footer == "" {
		return limitReply(toMrkdwn(reply.Text), maxReplyRunes)
	}
	footer := limitReply(toMrkdwn(reply.Footer), maxFooterRunes)
	room := maxReplyRunes - utf8.RuneCountInString(footer) - len("\n\n")
	return conversation.Reply{Text: limitReply(toMrkdwn(reply.Text), room), Footer: footer}.Plain()
}

// limitReply cuts text to at most limit runes, truncatedNote included.
func limitReply(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit-utf8.RuneCountInString(truncatedNote)]) + truncatedNote
}
