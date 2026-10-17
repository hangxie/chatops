// Package discord receives Discord messages over the Gateway and answers them as replies or in threads.
package discord

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/hangxie/chatops/internal/config"
	"github.com/hangxie/chatops/internal/conversation"
)

const (
	busyReply   = "I'm handling too many requests right now; please try again shortly."
	busyTimeout = 10 * time.Second
	// lookupTimeout bounds the REST lookup of a channel missing from the state cache.
	lookupTimeout = 5 * time.Second
	// maxBusyReplies bounds busy replies in flight; more are skipped rather than queued.
	maxBusyReplies = 4
)

// Adapter turns Discord gateway events into conversation messages.
type Adapter struct {
	gateway gateway
	logger  *slog.Logger
	botID   string
	// replyInThread answers top-level channel messages in a thread started on them.
	replyInThread bool

	busySlots chan struct{}
	busy      sync.WaitGroup
	connected atomic.Bool
}

// New builds an adapter from the bot token named in cfg.
func New(cfg config.ChatBackend, logger *slog.Logger) (*Adapter, error) {
	token, err := config.Secret(cfg.BotTokenEnv)
	if err != nil {
		return nil, fmt.Errorf("discord bot token: %w", err)
	}
	gw, err := newSession(token)
	if err != nil {
		return nil, fmt.Errorf("discord session: %w", err)
	}
	adapter := newAdapter(gw, logger)
	adapter.replyInThread = cfg.ReplyMode == config.ReplyThread
	return adapter, nil
}

func newAdapter(gw gateway, logger *slog.Logger) *Adapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Adapter{gateway: gw, logger: logger, busySlots: make(chan struct{}, maxBusyReplies)}
}

// Connected reports whether the gateway session is ready.
func (a *Adapter) Connected() bool { return a.connected.Load() }

// Run hands each new message to submit until ctx ends; the gateway reconnects on its own.
func (a *Adapter) Run(ctx context.Context, submit func(conversation.Message) error) error {
	defer a.connected.Store(false)
	opened, openDone := make(chan error, 1), make(chan struct{})
	// Open runs beside the event loop because it delivers READY before it returns.
	go func() {
		defer close(openDone)
		opened <- a.gateway.Open()
	}()
	err := a.consume(ctx, opened, submit)
	a.awaitOpen(openDone)
	if closeErr := a.gateway.Close(); closeErr != nil {
		a.logger.Debug("discord gateway close", "error", closeErr)
	}
	a.busy.Wait()
	return err
}

// awaitOpen drops events until Open returns, since a Close that wins the race leaves Open free to connect.
func (a *Adapter) awaitOpen(openDone <-chan struct{}) {
	for {
		select {
		case <-openDone:
			return
		case <-a.gateway.Events():
		}
	}
}

// consume handles events until ctx ends or the gateway fails to open; only an uncached channel waits on REST.
func (a *Adapter) consume(ctx context.Context, opened <-chan error, submit func(conversation.Message) error) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-opened:
			if err != nil {
				return fmt.Errorf("discord: open gateway: %w", err)
			}
			opened = nil
		case event := <-a.gateway.Events():
			a.handle(ctx, event, submit)
		}
	}
}

func (a *Adapter) handle(ctx context.Context, event any, submit func(conversation.Message) error) {
	switch e := event.(type) {
	case *discordgo.Ready:
		if e.User != nil {
			a.botID = e.User.ID
		}
		a.connected.Store(true)
		a.logger.Info("discord connected")
	case *discordgo.Resumed:
		a.connected.Store(true)
		a.logger.Info("discord resumed")
	case *discordgo.Disconnect:
		a.connected.Store(false)
		a.logger.Warn("discord disconnected")
	case *discordgo.MessageCreate:
		if in, ok := parseMessage(e.Message, a.botID, func(id string) bool { return a.isThread(ctx, id) }); ok {
			a.submit(ctx, in, e.SoftReference(), submit)
		}
	}
}

// isThread treats a channel whose type cannot be looked up as a regular channel, logging why.
func (a *Adapter) isThread(ctx context.Context, channelID string) bool {
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	thread, err := a.gateway.IsThread(ctx, channelID)
	if err != nil {
		a.logger.Warn("discord channel lookup failed; treating it as a channel", "channel", channelID, "error", err)
	}
	return thread
}

func (a *Adapter) submit(ctx context.Context, in inbound, ref *discordgo.MessageReference, submit func(conversation.Message) error) {
	log := a.logger.With("conversation", in.conversation(), "user", in.user)
	reply := a.replier(in.channel, ref)
	if a.replyInThread && !in.thread && in.guild != "" {
		reply = a.threadReplier(in, ref, log)
	}
	err := submit(conversation.Message{Conversation: in.conversation(), User: in.user, Text: in.text, Reply: reply})
	switch {
	case err == nil:
		log.Info("discord message accepted")
	case errors.Is(err, conversation.ErrBusy):
		log.Warn("discord message refused: busy")
		a.replyBusy(ctx, log, reply)
	default:
		log.Warn("discord message dropped", "error", err)
	}
}

// replyBusy posts the busy reply in the background, skipping it when maxBusyReplies are in flight.
func (a *Adapter) replyBusy(ctx context.Context, log *slog.Logger, reply func(context.Context, conversation.Reply) error) {
	select {
	case a.busySlots <- struct{}{}:
	default:
		log.Warn("discord busy reply skipped: too many in flight")
		return
	}
	a.busy.Go(func() {
		defer func() { <-a.busySlots }()
		ctx, cancel := context.WithTimeout(ctx, busyTimeout)
		defer cancel()
		if err := reply(ctx, conversation.Reply{Text: busyReply}); err != nil {
			log.Warn("discord busy reply failed", "error", err)
		}
	})
}

// replier answers as a reply to the user's message that pings no one and shows no link previews,
// so model output cannot mention @everyone or users, or make Discord fetch a URL.
func (a *Adapter) replier(channel string, ref *discordgo.MessageReference) func(context.Context, conversation.Reply) error {
	return func(ctx context.Context, reply conversation.Reply) error {
		for i, content := range messages(reply) {
			msg := &discordgo.MessageSend{
				Content:         content,
				AllowedMentions: &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}},
				Flags:           discordgo.MessageFlagsSuppressEmbeds,
			}
			if i == 0 {
				msg.Reference = ref
			}
			if err := a.gateway.Send(ctx, channel, msg); err != nil {
				return fmt.Errorf("discord: send reply: %w", err)
			}
		}
		return nil
	}
}
