package main

import (
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
		"version":       {args: []string{"version"}, command: "version"},
		"version-json":  {args: []string{"version", "--json"}, command: "version"},
		"no-args":       {args: nil, errMsg: `expected "version"`},
		"unknown":       {args: []string{"bogus"}, errMsg: "unexpected argument bogus"},
		"too-many-args": {args: []string{"version", "extra"}, errMsg: "unexpected argument extra"},
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

func Test_cli_run_version(t *testing.T) {
	parser, err := kong.New(&cli)
	require.NoError(t, err)

	ctx, err := parser.Parse([]string{"version"})
	require.NoError(t, err)

	stdout, stderr := testutils.CaptureStdoutStderr(func() {
		require.NoError(t, ctx.Run())
	})
	require.NotEmpty(t, stdout)
	require.Empty(t, stderr)
}
