package discord

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"

	"github.com/bwmarrin/discordgo"

	"github.com/hangxie/chatops/internal/conversation"
)

const (
	// maxThreadName is Discord's thread name limit, in characters.
	maxThreadName = 100
	// threadArchiveMinutes is one of the durations Discord accepts: 60, 1440, 4320, or 10080.
	threadArchiveMinutes = 1440
)

// threadReplier starts a thread on the message at the first reply, falling back to a channel reply if Discord refuses.
func (a *Adapter) threadReplier(in inbound, ref *discordgo.MessageReference, log *slog.Logger) func(context.Context, conversation.Reply) error {
	var (
		once  sync.Once
		reply func(context.Context, conversation.Reply) error
	)
	return func(ctx context.Context, r conversation.Reply) error {
		once.Do(func() { reply = a.startThread(ctx, in, ref, log) })
		return reply(ctx, r)
	}
}

func (a *Adapter) startThread(ctx context.Context, in inbound, ref *discordgo.MessageReference, log *slog.Logger) func(context.Context, conversation.Reply) error {
	thread, err := a.gateway.StartThread(ctx, in.channel, in.message, threadName(in.text))
	var restErr *discordgo.RESTError
	switch {
	case err == nil:
		return a.replier(thread, nil)
	case errors.As(err, &restErr) && restErr.Message != nil && restErr.Message.Code == discordgo.ErrCodeThreadAlreadyCreatedForThisMessage:
		// A thread started on a message has the message's ID.
		return a.replier(in.message, nil)
	default:
		log.Warn("discord thread start failed; replying in the channel", "error", err)
		return a.replier(in.channel, ref)
	}
}

// threadName is the question's first line, cut to Discord's limit.
func threadName(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	line = strings.TrimSpace(line)
	if line == "" {
		return "chatops"
	}
	if runes := []rune(line); len(runes) > maxThreadName {
		line = string(runes[:maxThreadName])
	}
	return line
}
