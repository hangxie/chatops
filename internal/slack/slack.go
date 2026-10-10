// Package slack receives Slack messages over Socket Mode and replies in their threads.
package slack

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/hangxie/chatops/internal/config"
	"github.com/hangxie/chatops/internal/conversation"
)

const (
	dedupeTTL   = time.Hour
	dedupeLimit = 10000
	// maxReplyRunes keeps escaped text (up to 5x longer) under Slack's 40,000-character limit.
	maxReplyRunes = 7000
	busyReply     = "I'm handling too many requests right now; please try again shortly."
	busyTimeout   = 10 * time.Second
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
	socket socketClient
	api    *slackapi.Client
	logger *slog.Logger
	now    func() time.Time
	seen   *seenSet
	botID  string

	busySlots chan struct{}
	busy      sync.WaitGroup
}

// New builds an adapter from the bot and app-level tokens named in cfg.
func New(cfg config.Slack, logger *slog.Logger) (*Adapter, error) {
	botToken, err := config.Secret(cfg.BotTokenEnv)
	if err != nil {
		return nil, fmt.Errorf("slack bot token: %w", err)
	}
	appToken, err := config.Secret(cfg.AppTokenEnv)
	if err != nil {
		return nil, fmt.Errorf("slack app token: %w", err)
	}
	api := slackapi.New(botToken, slackapi.OptionAppLevelToken(appToken))
	return newAdapter(socket{client: socketmode.New(api)}, api, logger), nil
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
	reply := a.replier(in.channel, in.thread)
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
func (a *Adapter) replyBusy(ctx context.Context, log *slog.Logger, reply func(context.Context, string) error) {
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
		if err := reply(ctx, busyReply); err != nil {
			log.Warn("slack busy reply failed", "error", err)
		}
	})
}

// replier posts escaped text as Slack mrkdwn without link previews, so model output cannot ping, link, or unfurl.
func (a *Adapter) replier(channel, thread string) func(context.Context, string) error {
	return func(ctx context.Context, text string) error {
		_, _, err := a.api.PostMessageContext(ctx, channel,
			slackapi.MsgOptionText(limitReply(toMrkdwn(text)), true),
			slackapi.MsgOptionTS(thread),
			slackapi.MsgOptionDisableLinkUnfurl(),
			slackapi.MsgOptionDisableMediaUnfurl())
		return err
	}
}

func limitReply(text string) string {
	runes := []rune(text)
	if len(runes) <= maxReplyRunes {
		return text
	}
	return string(runes[:maxReplyRunes]) + "\n[reply truncated]"
}
