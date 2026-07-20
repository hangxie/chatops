package main

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/alecthomas/kong"

	"github.com/hangxie/chatops/cmd/chats"
	"github.com/hangxie/chatops/cmd/server"
	"github.com/hangxie/chatops/cmd/version"
)

var cli struct {
	Chats   chats.Cmd   `cmd:"" help:"List available chat backends."`
	Server  server.Cmd  `cmd:"" help:"Run the ChatOps server."`
	Version version.Cmd `cmd:"" help:"Show build version."`
}

func main() {
	parser := newParser()
	runCtx, stop := terminationContext(context.Background())
	defer stop()
	parser.FatalIfErrorf(runCLI(parser, os.Args[1:], runCtx))
}

func newParser() *kong.Kong {
	return kong.Must(
		&cli,
		kong.Name("chatops"),
		kong.UsageOnError(),
		kong.ConfigureHelp(kong.HelpOptions{Compact: true}),
		kong.Description("ChatOps utility, for full usage see https://github.com/hangxie/chatops/blob/main/README.md"),
	)
}

func runCLI(parser *kong.Kong, args []string, runCtx context.Context) error {
	ctx, err := parser.Parse(args)
	if err != nil {
		return err
	}
	ctx.BindTo(runCtx, (*context.Context)(nil))
	return ctx.Run()
}

// terminationContext cancels on the first termination signal and restores
// the default signal behavior before making cancellation visible, allowing a
// second signal to terminate a process stuck during shutdown.
func terminationContext(parent context.Context) (context.Context, context.CancelFunc) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	return cancellationContext(parent, signals, func() { signal.Stop(signals) })
}

func cancellationContext(
	parent context.Context,
	signals <-chan os.Signal,
	restoreDefault func(),
) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	var restoreOnce sync.Once
	restore := func() { restoreOnce.Do(restoreDefault) }
	go func() {
		select {
		case <-signals:
			restore()
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, func() {
		restore()
		cancel()
	}
}
