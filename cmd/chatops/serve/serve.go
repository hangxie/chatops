// Package serve implements `chatops serve`, the chat-backend daemon.
package serve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"time"

	"github.com/hangxie/chatops/internal/agent"
	"github.com/hangxie/chatops/internal/chat"
	"github.com/hangxie/chatops/internal/config"
	"github.com/hangxie/chatops/internal/conversation"
	"github.com/hangxie/chatops/internal/llm"
	"github.com/hangxie/chatops/internal/mcp"
	"github.com/hangxie/chatops/internal/metrics"
	"github.com/hangxie/chatops/internal/version"
)

// shutdownGrace bounds how long in-flight turns and their replies may finish after a shutdown signal.
const shutdownGrace = 30 * time.Second

// errMetricsStopped marks an unexpected metrics server exit, which stops serve so a supervisor restarts it.
var errMetricsStopped = errors.New("metrics server stopped")

// Cmd is the kong command for serve.
type Cmd struct {
	Config    string `short:"c" required:"" placeholder:"FILE" help:"Path to the YAML config file."`
	LogLevel  string `default:"info" help:"Minimum level of logs written to stderr: debug, info, warn, or error."`
	LogFormat string `default:"text" help:"Log format on stderr: text or json."`

	// errOut, buildBackends, and listen override stderr, backend construction, and the metrics listener in tests.
	errOut        io.Writer
	buildBackends func(config.Chat, *slog.Logger) ([]chat.Named, error)
	listen        func(network, address string) (net.Listener, error)
}

// Run connects to the chat backends, the model, and the MCP servers, then answers messages until cancelled.
func (c Cmd) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	logger, err := c.logger()
	if err != nil {
		return err
	}
	cfg, err := config.Load(c.Config)
	if err != nil {
		return err
	}
	backends, err := c.builder()(cfg.Chat, logger)
	if err != nil {
		return err
	}
	models, err := llm.NewPool(cfg.LLM, nil, logger)
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

	// The metrics server stops after the deferred Shutdown below, so it stays up while turns drain.
	var observer conversation.Observer
	var exporter *metrics.Metrics
	if cfg.Metrics.Listen != "" {
		exporter = metrics.New(version.String())
		observer = exporter
		stop, err := c.serveMetrics(cfg.Metrics.Listen, exporter, logger, cancel)
		if err != nil {
			return err
		}
		defer stop()
	}

	// The manager's lifetime is independent of the backend contexts so that, once chat.Run returns (clean
	// shutdown or a fatal backend error), Shutdown can still drain accepted turns and deliver their replies.
	conversations := conversation.New(context.Background(), agent.New(models, tools, cfg.Agent, logger), cfg.Agent, logger, observer)
	defer conversations.Shutdown(shutdownGrace)
	if exporter != nil {
		exporter.Watch(metrics.Sources{
			Pending: conversations.Pending, InFlight: conversations.InFlight, Accepting: conversations.Accepting,
			Chat: chat.Health(backends), MCP: tools.Health,
		})
	}

	logger.Info("chatops serving", "backends", len(backends), "servers", len(cfg.MCP.Servers), "tools", len(tools.Catalog().Tools()))
	// Remove when per-user policy lands; until then deploy only read-only tools or trusted backends.
	logger.Warn("authorization is not enabled: every user who can message the bot can call every tool")
	err = chat.Run(ctx, backends, conversations.Submit)
	if cause := context.Cause(ctx); errors.Is(cause, errMetricsStopped) {
		return cause
	}
	return err
}

// serveMetrics serves exporter on address until stop is called; an unexpected exit calls fail with errMetricsStopped.
func (c Cmd) serveMetrics(address string, exporter *metrics.Metrics, logger *slog.Logger, fail context.CancelCauseFunc) (stop func(), err error) {
	listen := c.listen
	if listen == nil {
		listen = net.Listen
	}
	ln, err := listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("metrics: %w", err)
	}
	logger.Info("metrics serving", "address", ln.Addr().String())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := metrics.Serve(ctx, ln, exporter)
		switch {
		case err == nil:
		case ctx.Err() == nil:
			logger.Error("metrics server stopped", "error", err)
			fail(fmt.Errorf("%w: %w", errMetricsStopped, err))
		default:
			logger.Warn("metrics server shutdown", "error", err)
		}
	}()
	return func() { cancel(); <-done }, nil
}

func (c Cmd) builder() func(config.Chat, *slog.Logger) ([]chat.Named, error) {
	if c.buildBackends != nil {
		return c.buildBackends
	}
	return chat.Build
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
