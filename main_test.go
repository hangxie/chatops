package main

import (
	"context"
	"os"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/testutils"
)

func Test_cli_parse(t *testing.T) {
	tests := map[string]struct {
		args    []string
		errMsg  string
		command string
	}{
		"version":           {args: []string{"version"}, command: "version"},
		"version-json":      {args: []string{"version", "--json"}, command: "version"},
		"server":            {args: []string{"server", "--chat", "telnet://localhost:6023", "--planner", "ping://"}, command: "server"},
		"server-no-chat":    {args: []string{"server", "--planner", "ping://"}, errMsg: "--chat"},
		"server-no-planner": {args: []string{"server", "--chat", "telnet://localhost:6023"}, errMsg: "--planner"},
		"no-args":           {args: nil, errMsg: "expected one of"},
		"unknown":           {args: []string{"bogus"}, errMsg: "unexpected argument bogus"},
		"too-many-args":     {args: []string{"version", "extra"}, errMsg: "unexpected argument extra"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			parser, err := kong.New(&cli)
			require.NoError(t, err)

			ctx, err := parser.Parse(tc.args)
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.command, ctx.Command())
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

func Test_terminationContext_stop(t *testing.T) {
	ctx, stop := terminationContext(context.Background())
	stop()
	<-ctx.Done()
}

func Test_cli_run_version(t *testing.T) {
	stdout, stderr := testutils.CaptureStdoutStderr(func() {
		require.NoError(t, runCLI(newParser(), []string{"version"}, context.Background()))
	})
	require.NotEmpty(t, stdout)
	require.Empty(t, stderr)
}

func Test_cli_run_server_with_bound_context(t *testing.T) {
	err := runCLI(
		newParser(),
		[]string{"server", "--chat", "unknown://", "--planner", "ping://"},
		context.Background(),
	)
	require.ErrorContains(t, err, "open chat")
}

func Test_runCLI_parse_error(t *testing.T) {
	err := runCLI(newParser(), []string{"bogus"}, context.Background())
	require.ErrorContains(t, err, "unexpected argument")
}
