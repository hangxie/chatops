// Package serve implements `chatops serve`, the Slack daemon.
package serve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/hangxie/chatops/internal/agent"
	"github.com/hangxie/chatops/internal/config"
	"github.com/hangxie/chatops/internal/conversation"
	"github.com/hangxie/chatops/internal/llm"
	"github.com/hangxie/chatops/internal/mcp"
	"github.com/hangxie/chatops/internal/slack"
)

// receiver delivers chat messages until ctx ends; *slack.Adapter satisfies it.
type receiver interface {
	Run(ctx context.Context, submit func(conversation.Message) error) error
}

// Cmd is the kong command for serve.
type Cmd struct {
	Config    string `short:"c" required:"" placeholder:"FILE" help:"Path to the YAML config file."`
	LogLevel  string `default:"info" help:"Minimum level of logs written to stderr: debug, info, warn, or error."`
	LogFormat string `default:"text" help:"Log format on stderr: text or json."`

	// errOut and newReceiver override stderr and the Slack adapter in tests.
	errOut      io.Writer
	newReceiver func(config.Slack, *slog.Logger) (receiver, error)
}

// Run connects to Slack, the model, and MCP servers, then answers messages until cancelled.
func (c Cmd) Run(ctx context.Context) error {
	logger, err := c.logger()
	if err != nil {
		return err
	}
	cfg, err := config.Load(c.Config)
	if err != nil {
		return err
	}
	if cfg.Chat.Slack == nil {
		return errors.New("serve requires chat.slack in the config")
	}
	chat, err := c.receiverFactory()(*cfg.Chat.Slack, logger)
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

	turnCtx, cancelTurns := context.WithCancel(ctx)
	conversations := conversation.New(turnCtx, agent.New(model, tools, cfg.Agent, logger), cfg.Agent, logger)
	defer func() {
		cancelTurns()
		conversations.Wait()
	}()
	logger.Info("chatops serving", "servers", len(cfg.MCP.Servers), "tools", len(tools.Catalog().Tools()))
	// Remove when per-user policy lands; until then deploy only read-only tools or trusted channels.
	logger.Warn("authorization is not enabled: every user who can message the bot can call every tool")
	return chat.Run(ctx, conversations.Submit)
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

func (c Cmd) receiverFactory() func(config.Slack, *slog.Logger) (receiver, error) {
	if c.newReceiver != nil {
		return c.newReceiver
	}
	return func(cfg config.Slack, logger *slog.Logger) (receiver, error) {
		return slack.New(cfg, logger)
	}
}
