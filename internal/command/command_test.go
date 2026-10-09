package command

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

type contextCmd struct {
	got context.Context
	err error
}

func (c *contextCmd) Run(ctx context.Context) error {
	c.got = ctx
	return c.err
}

type testCLI struct {
	Probe contextCmd `cmd:"" help:"Record the bound context."`
}

func Test_NewParser(t *testing.T) {
	var cli testCLI
	parser := NewParser(&cli, "demo", "Demo description.")
	require.Equal(t, "demo", parser.Model.Name)
	require.Equal(t, "Demo description.", parser.Model.Help)
}

func Test_Run(t *testing.T) {
	type ctxKey struct{}
	cmdErr := errors.New("boom")

	tests := map[string]struct {
		args   []string
		cmdErr error
		errMsg string
		ran    bool
	}{
		"runs-with-bound-context": {args: []string{"probe"}, ran: true},
		"command-error":           {args: []string{"probe"}, cmdErr: cmdErr, errMsg: "boom", ran: true},
		"no-args":                 {args: nil, errMsg: "expected"},
		"unknown":                 {args: []string{"bogus"}, errMsg: "unexpected argument bogus"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var cli testCLI
			cli.Probe.err = tc.cmdErr
			ctx := context.WithValue(context.Background(), ctxKey{}, name)

			err := Run(ctx, NewParser(&cli, "demo", "Demo."), tc.args)
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
			} else {
				require.NoError(t, err)
			}
			if tc.ran {
				require.NotNil(t, cli.Probe.got)
				require.Equal(t, name, cli.Probe.got.Value(ctxKey{}))
			} else {
				require.Nil(t, cli.Probe.got)
			}
		})
	}
}

func Test_cancellationContext_restores_signals_before_cancelling(t *testing.T) {
	signals := make(chan os.Signal, 1)
	var restored atomic.Bool
	ctx, stop := cancellationContext(context.Background(), signals, func() { restored.Store(true) })
	defer stop()

	signals <- syscall.SIGTERM
	<-ctx.Done()
	require.True(t, restored.Load())
}

func Test_cancellationContext_stop(t *testing.T) {
	var restoreCalls atomic.Int32
	ctx, stop := cancellationContext(context.Background(), make(chan os.Signal), func() { restoreCalls.Add(1) })
	stop()
	stop()
	<-ctx.Done()
	require.Equal(t, int32(1), restoreCalls.Load())
}

func Test_TerminationContext_stop(t *testing.T) {
	ctx, stop := TerminationContext(context.Background())
	stop()
	<-ctx.Done()
}
