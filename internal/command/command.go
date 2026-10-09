// Package command runs a kong command line with signal-aware cancellation for every binary.
package command

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/alecthomas/kong"
)

// NewParser builds the kong parser for a binary's cli struct.
func NewParser(cli any, name, description string) *kong.Kong {
	return kong.Must(
		cli,
		kong.Name(name),
		kong.UsageOnError(),
		kong.ConfigureHelp(kong.HelpOptions{Compact: true}),
		kong.Description(description),
	)
}

// Run parses args and runs the selected subcommand with ctx bound for its Run method.
func Run(ctx context.Context, parser *kong.Kong, args []string) error {
	kctx, err := parser.Parse(args)
	if err != nil {
		return err
	}
	kctx.BindTo(ctx, (*context.Context)(nil))
	return kctx.Run()
}

// TerminationContext cancels on the first SIGINT/SIGTERM; a second one kills a stuck shutdown.
func TerminationContext(parent context.Context) (context.Context, context.CancelFunc) {
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
