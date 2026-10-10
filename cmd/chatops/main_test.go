package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/command"
	"github.com/hangxie/chatops/internal/testutils"
)

func Test_cli_parse(t *testing.T) {
	tests := map[string]struct {
		args    []string
		errMsg  string
		command string
	}{
		"chat":            {args: []string{"chat", "--config", "c.yaml"}, command: "chat"},
		"chat-short":      {args: []string{"chat", "-c", "c.yaml", "--log-level", "debug"}, command: "chat"},
		"chat-no-config":  {args: []string{"chat"}, errMsg: "missing flags: --config=FILE"},
		"serve":           {args: []string{"serve", "-c", "c.yaml", "--log-format", "json"}, command: "serve"},
		"serve-no-config": {args: []string{"serve"}, errMsg: "missing flags: --config=FILE"},
		"version":         {args: []string{"version"}, command: "version"},
		"version-json":    {args: []string{"version", "--json"}, command: "version"},
		"no-args":         {args: nil, errMsg: "expected"},
		"unknown":         {args: []string{"bogus"}, errMsg: "unexpected argument bogus"},
		"too-many-args":   {args: []string{"version", "extra"}, errMsg: "unexpected argument extra"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, err := newParser().Parse(tc.args)
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.command, ctx.Command())
		})
	}
}

func Test_newParser_name(t *testing.T) {
	require.Equal(t, "chatops", newParser().Model.Name)
}

func Test_cli_run_version(t *testing.T) {
	stdout, stderr := testutils.CaptureStdoutStderr(func() {
		require.NoError(t, command.Run(context.Background(), newParser(), []string{"version"}))
	})
	require.NotEmpty(t, stdout)
	require.Empty(t, stderr)
}
