// Package server implements the long-running chatops server command.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/hangxie/chatops/chat"
	"github.com/hangxie/chatops/chat/telnet"
	"github.com/hangxie/chatops/cred"
	"github.com/hangxie/chatops/cred/jsonfile"
	"github.com/hangxie/chatops/engine"
	"github.com/hangxie/chatops/planner"
	plannerping "github.com/hangxie/chatops/planner/ping"
	"github.com/hangxie/chatops/tool"
	toolping "github.com/hangxie/chatops/tool/ping"
)

// Cmd contains all configuration for one engine server.
type Cmd struct {
	ChatURL        string `name:"chat" required:"" help:"Chat backend URL (for example, telnet://localhost:6023)."`
	PlannerURL     string `name:"planner" required:"" help:"Planner backend URL (for example, ping://)."`
	CredentialsURL string `name:"credentials" help:"Optional credential store URL for planners and tools."`
	ConnectionID   string `name:"connection-id" default:"default" help:"Stable ID used to scope planner conversation state."`
	MaxConcurrency int    `name:"max-concurrency" default:"8" help:"Maximum number of conversations processed concurrently."`
}

// Run starts the engine and gracefully stops when ctx is cancelled.
func (c *Cmd) Run(ctx context.Context) error {
	return c.run(ctx)
}

func (c *Cmd) run(ctx context.Context) (err error) {
	var credentials cred.Store
	if c.CredentialsURL != "" {
		credentials, err = credentialRegistry().Open(ctx, c.CredentialsURL)
		if err != nil {
			return fmt.Errorf("server: open credentials: %w", err)
		}
		defer func() {
			err = errors.Join(err, closeNamed("credentials", credentials))
		}()
	}

	conn, err := chatRegistry().Open(ctx, c.ChatURL)
	if err != nil {
		return fmt.Errorf("server: open chat: %w", err)
	}
	p, err := plannerRegistry().Open(ctx, c.PlannerURL, credentials)
	if err != nil {
		return errors.Join(fmt.Errorf("server: open planner: %w", err), closeNamed("chat", conn))
	}
	e, err := engine.New(engine.Config{
		ConnectionID:   c.ConnectionID,
		Chat:           conn,
		Planner:        p,
		Tools:          toolRegistry(),
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

func credentialRegistry() *cred.Registry {
	return cred.NewRegistry(cred.Backend{Scheme: jsonfile.Scheme, Opener: jsonfile.Opener})
}

func chatRegistry() *chat.Registry {
	return chat.NewRegistry(chat.Backend{Scheme: telnet.Scheme, Opener: telnet.Opener})
}

func plannerRegistry() *planner.Registry {
	return planner.NewRegistry(planner.Backend{Scheme: plannerping.Scheme, Opener: plannerping.Opener})
}

func toolRegistry() *tool.Registry {
	return tool.NewRegistry(tool.Backend{Scheme: toolping.Scheme, Opener: toolping.Opener})
}

func closeNamed(name string, closer io.Closer) error {
	if err := closer.Close(); err != nil {
		return fmt.Errorf("server: close %s: %w", name, err)
	}
	return nil
}
