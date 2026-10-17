// Package chat selects and runs the daemon's chat backends against one conversation manager.
package chat

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/hangxie/chatops/internal/chat/discord"
	"github.com/hangxie/chatops/internal/chat/slack"
	"github.com/hangxie/chatops/internal/chat/stdio"
	"github.com/hangxie/chatops/internal/chat/unix"
	"github.com/hangxie/chatops/internal/config"
	"github.com/hangxie/chatops/internal/conversation"
)

// Backend is one source of inbound messages; Run delivers each by calling submit and returns when the
// backend stops (nil on a clean stop, an error on an unrecoverable failure).
type Backend interface {
	Run(ctx context.Context, submit func(conversation.Message) error) error
	// Connected reports whether the backend can receive messages right now.
	Connected() bool
}

// Named pairs a backend with its configured name, which prefixes the backend's conversation keys.
type Named struct {
	Name    string
	Backend Backend
}

// BackendHealth is one chat backend's connection state.
type BackendHealth struct {
	Name string
	Up   bool
}

// Health returns a reader of every backend's connection state, in the given order.
func Health(backends []Named) func() []BackendHealth {
	return func() []BackendHealth {
		health := make([]BackendHealth, 0, len(backends))
		for _, b := range backends {
			health = append(health, BackendHealth{Name: b.Name, Up: b.Backend.Connected()})
		}
		return health
	}
}

// New builds one backend from its definition, switching on the configured type.
func New(name string, cfg config.ChatBackend, logger *slog.Logger) (Backend, error) {
	switch cfg.Type {
	case config.ChatSlack:
		return slack.New(cfg, logger)
	case config.ChatDiscord:
		return discord.New(cfg, logger)
	case config.ChatStdio:
		return stdio.New(cfg, logger), nil
	case config.ChatUnix:
		return unix.New(cfg, logger)
	default:
		return nil, fmt.Errorf("chat backend %q: unknown type %q", name, cfg.Type)
	}
}

// Select builds the backends named by names, in that order, failing if a name is absent or invalid.
func Select(cfg config.Chat, names []string, logger *slog.Logger) ([]Named, error) {
	selected := make([]Named, 0, len(names))
	for _, name := range names {
		def, ok := cfg[name]
		if !ok {
			return nil, fmt.Errorf("chat backend %q is not defined in the config", name)
		}
		backend, err := New(name, def, logger)
		if err != nil {
			return nil, err
		}
		selected = append(selected, Named{Name: name, Backend: backend})
	}
	return selected, nil
}
