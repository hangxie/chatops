// Package serve implements `chatops serve`, the chat-backend daemon.
package serve

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/hangxie/chatops/internal/agent"
	"github.com/hangxie/chatops/internal/chat"
	"github.com/hangxie/chatops/internal/config"
	"github.com/hangxie/chatops/internal/conversation"
	"github.com/hangxie/chatops/internal/llm"
	"github.com/hangxie/chatops/internal/mcp"
)

// shutdownGrace bounds how long in-flight turns and their replies may finish after a shutdown signal.
const shutdownGrace = 30 * time.Second

// Cmd is the kong command for serve.
type Cmd struct {
	Config    string   `short:"c" required:"" placeholder:"FILE" help:"Path to the YAML config file."`
	Chat      []string `required:"" sep:"," placeholder:"NAME" help:"Comma-separated chat backend names to run, from the config's chat: map."`
	LogLevel  string   `default:"info" help:"Minimum level of logs written to stderr: debug, info, warn, or error."`
	LogFormat string   `default:"text" help:"Log format on stderr: text or json."`

	// errOut and selectBackends override stderr and backend construction in tests.
	errOut         io.Writer
	selectBackends func(config.Chat, []string, *slog.Logger) ([]chat.Named, error)
}

// Run connects to the chat backends, the model, and the MCP servers, then answers messages until cancelled.
func (c Cmd) Run(ctx context.Context) error {
	logger, err := c.logger()
	if err != nil {
		return err
	}
	cfg, err := config.Load(c.Config)
	if err != nil {
		return err
	}
	backends, err := c.selector()(cfg.Chat, c.Chat, logger)
	if err != nil {
		return err
	}
	model, err := llm.New(cfg.LLM, nil)
	if err != nil {
		return err
	}
	tools, err := mcp.Open(ctx, cfg.MCP.Servers, logger)
	if err != nil {
		return err
	}
	defer func() {
		if err := tools.Close(); err != nil {
			logger.Warn("close mcp servers", "error", err)
		}
	}()

	// The manager's lifetime is independent of the backend contexts so that, once chat.Run returns (clean
	// shutdown or a fatal backend error), Shutdown can still drain accepted turns and deliver their replies.
	conversations := conversation.New(context.Background(), agent.New(model, tools, cfg.Agent, logger), cfg.Agent, logger, nil)
	defer conversations.Shutdown(shutdownGrace)

	logger.Info("chatops serving", "backends", len(backends), "servers", len(cfg.MCP.Servers), "tools", len(tools.Catalog().Tools()))
	// Remove when per-user policy lands; until then deploy only read-only tools or trusted backends.
	logger.Warn("authorization is not enabled: every user who can message the bot can call every tool")
	return chat.Run(ctx, backends, conversations.Submit)
}

func (c Cmd) selector() func(config.Chat, []string, *slog.Logger) ([]chat.Named, error) {
	if c.selectBackends != nil {
		return c.selectBackends
	}
	return chat.Select
}

func (c Cmd) logger() (*slog.Logger, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(c.LogLevel)); err != nil {
		return nil, fmt.Errorf("log level: %w", err)
	}
	options := &slog.HandlerOptions{Level: level}
	switch c.LogFormat {
	case "text":
		return slog.New(slog.NewTextHandler(c.logOutput(), options)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(c.logOutput(), options)), nil
	}
	return nil, fmt.Errorf("log format: must be \"text\" or \"json\", got %q", c.LogFormat)
}

func (c Cmd) logOutput() io.Writer {
	if c.errOut != nil {
		return c.errOut
	}
	return os.Stderr
}
