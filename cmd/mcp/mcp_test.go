package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/stretchr/testify/require"
)

// withClosedStdin points os.Stdin at an already-closed pipe, so a stdio
// server ends its session immediately instead of blocking the test.
func withClosedStdin(t *testing.T) {
	t.Helper()
	saved := os.Stdin
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	os.Stdin = reader
	t.Cleanup(func() { os.Stdin = saved })
}

func Test_ServeCmd_parses_arguments(t *testing.T) {
	testCases := map[string]struct {
		args   []string
		want   ServeCmd
		errMsg string
	}{
		"group-only": {
			args: []string{"serve", "ping"},
			want: ServeCmd{Group: "ping"},
		},
		"group-with-options": {
			args: []string{"serve", "k8s?context=prod"},
			want: ServeCmd{Group: "k8s?context=prod"},
		},
		"with-credentials": {
			args: []string{"serve", "ping", "--credentials", "json-file:///tmp/c.json"},
			want: ServeCmd{Group: "ping", CredentialsURL: "json-file:///tmp/c.json"},
		},
		"missing-group": {args: []string{"serve"}, errMsg: "group"},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			var command Cmd
			parser, err := kong.New(&command)
			require.NoError(t, err)
			_, err = parser.Parse(tc.args)
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, command.Serve)
		})
	}
}

func Test_ServeCmd_Run_serves_until_stdin_closes(t *testing.T) {
	withClosedStdin(t)

	// A host ends a stdio session by closing the pipe, which is an ordinary
	// stop rather than a failure.
	command := &ServeCmd{Group: "ping"}
	require.NoError(t, command.Run(context.Background()))
}

func Test_ServeCmd_Run_accepts_group_options(t *testing.T) {
	withClosedStdin(t)

	command := &ServeCmd{Group: "status"}
	require.NoError(t, command.Run(context.Background()))
}

func Test_ServeCmd_Run_errors(t *testing.T) {
	testCases := map[string]struct {
		command ServeCmd
		errMsg  string
	}{
		"unknown-group":   {command: ServeCmd{Group: "nope"}, errMsg: "unknown built-in group"},
		"bad-selector":    {command: ServeCmd{Group: "ping://"}, errMsg: "must be a bare name"},
		"bad-option":      {command: ServeCmd{Group: "ping?loud=true"}, errMsg: "unknown option"},
		"bad-credentials": {command: ServeCmd{Group: "ping", CredentialsURL: "unknown://"}, errMsg: "open credentials"},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			require.ErrorContains(t, tc.command.Run(context.Background()), tc.errMsg)
		})
	}
}

func Test_ServeCmd_Run_with_credential_store(t *testing.T) {
	withClosedStdin(t)

	path := filepath.Join(t.TempDir(), "creds.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"planner":{"api-key":"sk-test"}}`), 0o600))

	command := &ServeCmd{Group: "ping", CredentialsURL: "json-file://" + path}
	require.NoError(t, command.Run(context.Background()))
}

func Test_ServeCmd_Run_reports_an_unusable_stdin(t *testing.T) {
	saved := os.Stdin
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.NoError(t, reader.Close())
	os.Stdin = reader
	defer func() { os.Stdin = saved }()

	// A stdin that cannot be read at all is a real failure rather than the
	// ordinary end of a session, so it is reported.
	err = (&ServeCmd{Group: "ping"}).Run(context.Background())
	require.ErrorContains(t, err, `mcp: serve "ping"`)
}

func Test_ServeCmd_Run_cancelled_context(t *testing.T) {
	withClosedStdin(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Cancellation is a graceful stop, so a terminated server reports no error.
	require.NoError(t, (&ServeCmd{Group: "ping"}).Run(ctx))
}
