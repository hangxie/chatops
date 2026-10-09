// Package command runs a kong command line with signal-aware cancellation,
// shared by every binary under cmd/.
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

// Run parses args and runs the selected subcommand, binding ctx so commands
// can accept a context.Context parameter.
func Run(ctx context.Context, parser *kong.Kong, args []string) error {
	kctx, err := parser.Parse(args)
	if err != nil {
		return err
	}
	kctx.BindTo(ctx, (*context.Context)(nil))
	return kctx.Run()
}

// TerminationContext cancels on the first termination signal and restores
// the default signal behavior before making cancellation visible, allowing a
// second signal to terminate a process stuck during shutdown.
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
