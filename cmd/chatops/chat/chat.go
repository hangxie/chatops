// Package chat implements `chatops chat`, a single-conversation terminal harness for the agent.
package chat

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/hangxie/chatops/internal/agent"
	"github.com/hangxie/chatops/internal/config"
	"github.com/hangxie/chatops/internal/llm"
	"github.com/hangxie/chatops/internal/mcp"
)

// Cmd is the kong command for chat.
type Cmd struct {
	Config   string `short:"c" required:"" placeholder:"FILE" help:"Path to the YAML config file."`
	LogLevel string `default:"warn" help:"Minimum level of logs written to stderr: debug, info, warn, or error."`

	// in, out, and errOut override stdin, stdout, and stderr in tests.
	in     io.Reader
	out    io.Writer
	errOut io.Writer
}

// Run connects to the model and MCP servers, then chats until EOF, /quit, or cancellation.
func (c Cmd) Run(ctx context.Context) error {
	in, out, errOut := c.streams()
	var level slog.Level
	if err := level.UnmarshalText([]byte(c.LogLevel)); err != nil {
		return fmt.Errorf("log level: %w", err)
	}
	logger := slog.New(slog.NewTextHandler(errOut, &slog.HandlerOptions{Level: level}))

	cfg, err := config.Load(c.Config)
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

	return repl(ctx, agent.New(model, tools, cfg.Agent, logger), cfg.Agent.HistoryTurns, in, out)
}

func (c Cmd) streams() (io.Reader, io.Writer, io.Writer) {
	in, out, errOut := c.in, c.out, c.errOut
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stdout
	}
	if errOut == nil {
		errOut = os.Stderr
	}
	return in, out, errOut
}
