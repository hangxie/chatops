// Package server implements the long-running chatops server command.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/hangxie/chatops/chat"
	"github.com/hangxie/chatops/cred"
	"github.com/hangxie/chatops/engine"
	"github.com/hangxie/chatops/internal/builtin"
	"github.com/hangxie/chatops/internal/registry"
	"github.com/hangxie/chatops/mcphost"
	"github.com/hangxie/chatops/tool/reply"
)

// Cmd contains all configuration for one engine server.
type Cmd struct {
	ChatURL        string   `name:"chat" required:"" help:"Chat backend URL (for example, slack:// or telnet://localhost:6023)."`
	PlannerURL     string   `name:"planner" required:"" help:"Planner backend URL (for example, ping://)."`
	CredentialsURL string   `name:"credentials" help:"Optional credential store URL for chat backends, planners, and tools."`
	ConnectionID   string   `name:"connection-id" default:"default" help:"Stable ID used to scope planner conversation state."`
	MaxConcurrency int      `name:"max-concurrency" default:"8" help:"Maximum number of conversations processed concurrently."`
	Builtin        []string `name:"builtin" help:"Built-in tool group to serve, as a bare name (k8s) or a name with options (k8s?context=prod); repeat for several groups (default: all)."`
	Tools          []string `name:"tool" help:"Tool name to expose, with '*' and '?' wildcards (k8s-*); repeat to allow several (default: all)."`
	LogLevel       string   `name:"log-level" enum:"debug,info,warn,error" default:"info" help:"Log verbosity: debug, info, warn, or error."`
	LogFormat      string   `name:"log-format" enum:"json,text" default:"json" help:"Log output format: json or text."`
}

// Run starts the engine and gracefully stops when ctx is cancelled.
func (c *Cmd) Run(ctx context.Context) error {
	return c.run(ctx)
}

func (c *Cmd) run(ctx context.Context) (err error) {
	logger, err := newLogger(c.LogLevel, c.LogFormat, os.Stderr)
	if err != nil {
		return err
	}
	slog.SetDefault(logger)

	logger.Info(
		"starting server",
		"chat", c.ChatURL,
		"planner", c.PlannerURL,
		"builtin", c.Builtin,
		"connection_id", c.ConnectionID,
		"max_concurrency", c.MaxConcurrency,
	)

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

	// Tool configuration is validated before any backend is opened, so an
	// operator's typo fails immediately rather than after a chat connection
	// has been established.
	servers, err := builtin.Servers(c.Builtin, credentials, logger)
	if err != nil {
		return fmt.Errorf("server: configure tools: %w", err)
	}
	if err := mcphost.CheckPatterns(c.Tools); err != nil {
		return fmt.Errorf("server: configure tools: %w", err)
	}

	conn, err := registry.Chat().Open(ctx, c.ChatURL, credentials)
	if err != nil {
		return fmt.Errorf("server: open chat: %w", err)
	}
	host, err := c.openHost(ctx, conn, servers, logger)
	if err != nil {
		return errors.Join(fmt.Errorf("server: open tools: %w", err), closeNamed("chat", conn))
	}
	logger.Info("tools ready", "tools", host.Names())

	p, err := registry.Planner().Open(ctx, c.PlannerURL, credentials, host)
	if err != nil {
		return errors.Join(
			fmt.Errorf("server: open planner: %w", err),
			closeNamed("tools", host),
			closeNamed("chat", conn),
		)
	}
	e, err := engine.New(engine.Config{
		ConnectionID:   c.ConnectionID,
		Chat:           conn,
		Planner:        p,
		Tools:          host,
		MaxConcurrency: c.MaxConcurrency,
		Logger:         logger,
	})
	if err != nil {
		return errors.Join(
			fmt.Errorf("server: initialize engine: %w", err),
			closeNamed("planner", p),
			closeNamed("tools", host),
			closeNamed("chat", conn),
		)
	}
	if err := e.Run(ctx); err != nil {
		return fmt.Errorf("server: run engine: %w", err)
	}
	return nil
}

// openHost connects the built-in MCP servers and registers the reply tool,
// which is served by this process because it posts into the live chat
// connection rather than acting on a remote endpoint.
func (c *Cmd) openHost(ctx context.Context, conn chat.Conn, servers []mcphost.ServerSpec, logger *slog.Logger) (*mcphost.Host, error) {
	replyTool := reply.Open(ctx, conn)
	return mcphost.New(ctx, mcphost.Config{
		Servers:   servers,
		HostTools: []mcphost.HostTool{{Def: reply.Definition(), Handler: replyTool.Handler}},
		Allow:     c.Tools,
		Logger:    logger,
	})
}

// newLogger builds the structured logger from the configured level and
// format, writing to w. An empty level or format uses the defaults (info,
// json) so a directly constructed Cmd — bypassing kong's flag defaults —
// still logs. Unknown values are rejected.
func newLogger(level, format string, w io.Writer) (*slog.Logger, error) {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "", "info":
		lvl = slog.LevelInfo
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		return nil, fmt.Errorf("server: unknown log level %q (want debug, info, warn, or error)", level)
	}
	opts := &slog.HandlerOptions{Level: lvl}
	switch strings.ToLower(format) {
	case "", "json":
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	case "text":
		return slog.New(slog.NewTextHandler(w, opts)), nil
	default:
		return nil, fmt.Errorf("server: unknown log format %q (want json or text)", format)
	}
}

func closeNamed(name string, closer io.Closer) error {
	if err := closer.Close(); err != nil {
		return fmt.Errorf("server: close %s: %w", name, err)
	}
	return nil
}
