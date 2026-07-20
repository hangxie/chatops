// Package server implements the long-running chatops server command.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/hangxie/chatops/cred"
	"github.com/hangxie/chatops/engine"
	"github.com/hangxie/chatops/internal/registry"
)

// Cmd contains all configuration for one engine server.
type Cmd struct {
	ChatURL        string   `name:"chat" required:"" help:"Chat backend URL (for example, slack:// or telnet://localhost:6023)."`
	PlannerURL     string   `name:"planner" required:"" help:"Planner backend URL (for example, ping://)."`
	CredentialsURL string   `name:"credentials" help:"Optional credential store URL for planners and tools."`
	ConnectionID   string   `name:"connection-id" default:"default" help:"Stable ID used to scope planner conversation state."`
	MaxConcurrency int      `name:"max-concurrency" default:"8" help:"Maximum number of conversations processed concurrently."`
	Tools          []string `name:"tool" help:"Selectable tool to expose; repeat to allow multiple tools (default: all)."`
}

// Run starts the engine and gracefully stops when ctx is cancelled.
func (c *Cmd) Run(ctx context.Context) error {
	return c.run(ctx)
}

func (c *Cmd) run(ctx context.Context) (err error) {
	tools := registry.Tool()
	if len(c.Tools) != 0 {
		tools, err = tools.Select(c.Tools...)
		if err != nil {
			return fmt.Errorf("server: configure tools: %w", err)
		}
	}

	var credentials cred.Store
	if c.CredentialsURL != "" {
		credentials, err = registry.Credential().Open(ctx, c.CredentialsURL)
		if err != nil {
			return fmt.Errorf("server: open credentials: %w", err)
		}
		defer func() {
			err = errors.Join(err, closeNamed("credentials", credentials))
		}()
	}

	conn, err := registry.Chat().Open(ctx, c.ChatURL)
	if err != nil {
		return fmt.Errorf("server: open chat: %w", err)
	}
	p, err := registry.Planner().Open(ctx, c.PlannerURL, credentials)
	if err != nil {
		return errors.Join(fmt.Errorf("server: open planner: %w", err), closeNamed("chat", conn))
	}
	e, err := engine.New(engine.Config{
		ConnectionID:   c.ConnectionID,
		Chat:           conn,
		Planner:        p,
		Tools:          tools,
		Credentials:    credentials,
		MaxConcurrency: c.MaxConcurrency,
	})
	if err != nil {
		return errors.Join(
			fmt.Errorf("server: initialize engine: %w", err),
			closeNamed("planner", p),
			closeNamed("chat", conn),
		)
	}
	if err := e.Run(ctx); err != nil {
		return fmt.Errorf("server: run engine: %w", err)
	}
	return nil
}

func closeNamed(name string, closer io.Closer) error {
	if err := closer.Close(); err != nil {
		return fmt.Errorf("server: close %s: %w", name, err)
	}
	return nil
}
