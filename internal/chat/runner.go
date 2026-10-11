package chat

import (
	"context"

	"golang.org/x/sync/errgroup"

	"github.com/hangxie/chatops/internal/conversation"
)

// Run starts every backend at once, each submitting into submit, and returns when all have stopped or the
// first one fails. A backend returning nil ends only itself; the others keep running until they return too.
func Run(ctx context.Context, backends []Named, submit func(conversation.Message) error) error {
	group, ctx := errgroup.WithContext(ctx)
	for _, b := range backends {
		group.Go(func() error { return b.Backend.Run(ctx, prefixed(b.Name, submit)) })
	}
	return group.Wait()
}

// prefixed namespaces a backend's conversation keys with its name so keys from different backends cannot collide.
func prefixed(name string, submit func(conversation.Message) error) func(conversation.Message) error {
	return func(msg conversation.Message) error {
		msg.Conversation = name + ":" + msg.Conversation
		return submit(msg)
	}
}
