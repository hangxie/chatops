package serve

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func toolNames(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()
	result, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	names := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func newClient() *mcp.Client {
	return mcp.NewClient(&mcp.Implementation{Name: "client", Version: "v0"}, nil)
}

func Test_newServer(t *testing.T) {
	tests := map[string]struct {
		groups []string
		tools  []string
		errMsg string
	}{
		"ping":           {groups: []string{"ping"}, tools: []string{"ping"}},
		"duplicate":      {groups: []string{"ping", "ping"}, errMsg: `tool group "ping" listed more than once`},
		"unknown":        {groups: []string{"bogus"}, errMsg: `unknown tool group "bogus" (available: ping)`},
		"none":           {groups: nil, errMsg: "at least one tool group is required"},
		"blank-is-bogus": {groups: []string{""}, errMsg: `unknown tool group "" (available: ping)`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			server, err := newServer(tc.groups)
			if tc.errMsg != "" {
				require.EqualError(t, err, tc.errMsg)
				require.Nil(t, server)
				return
			}
			require.NoError(t, err)

			serverTransport, clientTransport := mcp.NewInMemoryTransports()
			serverSession, err := server.Connect(context.Background(), serverTransport, nil)
			require.NoError(t, err)
			defer func() { _ = serverSession.Close() }()
			session, err := newClient().Connect(context.Background(), clientTransport, nil)
			require.NoError(t, err)
			defer func() { _ = session.Close() }()

			require.Equal(t, "chatops-mcp", session.InitializeResult().ServerInfo.Name)
			require.Equal(t, tc.tools, toolNames(t, session))
		})
	}
}

func Test_Cmd_Run_transport(t *testing.T) {
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Cmd{Tools: []string{"ping"}, transport: serverTransport}.Run(ctx)
	}()

	session, err := newClient().Connect(context.Background(), clientTransport, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"ping"}, toolNames(t, session))
	require.NoError(t, session.Close())
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop")
	}
}

func Test_Cmd_Run_invalid_group(t *testing.T) {
	err := Cmd{Tools: []string{"bogus"}}.Run(context.Background())
	require.ErrorContains(t, err, `unknown tool group "bogus"`)
}

func Test_Cmd_Run_http(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Setenv("SERVE_TEST_TOKEN", "s3cret")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	cmd := Cmd{Tools: []string{"ping"}, HTTP: listener.Addr().String(), TokenEnv: "SERVE_TEST_TOKEN", listener: listener}
	go func() { done <- cmd.Run(ctx) }()

	endpoint := "http://" + listener.Addr().String() + "/mcp"
	_, err = connectHTTP(endpoint, "")
	require.ErrorContains(t, err, "Unauthorized")
	session, err := connectHTTP(endpoint, "s3cret")
	require.NoError(t, err)
	require.Equal(t, []string{"ping"}, toolNames(t, session))
	require.NoError(t, session.Close())
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop")
	}
}

func Test_Cmd_Run_errors(t *testing.T) {
	tests := map[string]struct {
		cmd    Cmd
		errMsg string
	}{
		"listen":              {cmd: Cmd{Tools: []string{"ping"}, HTTP: "127.0.0.1:99999"}, errMsg: "serve http: listen tcp: address 99999: invalid port"},
		"non-loopback":        {cmd: Cmd{Tools: []string{"ping"}, HTTP: ":0"}, errMsg: `refusing to serve http on non-loopback address ":0" without --token-env`},
		"token-env-unset":     {cmd: Cmd{Tools: []string{"ping"}, HTTP: "127.0.0.1:0", TokenEnv: "SERVE_TEST_TOKEN_UNSET"}, errMsg: "environment variable SERVE_TEST_TOKEN_UNSET is unset or empty"},
		"token-env-for-stdio": {cmd: Cmd{Tools: []string{"ping"}, TokenEnv: "SERVE_TEST_TOKEN"}, errMsg: "--token-env requires --http"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := tc.cmd.Run(context.Background())
			require.ErrorContains(t, err, tc.errMsg)
		})
	}
}

type failingTransport struct{}

func (failingTransport) Connect(context.Context) (mcp.Connection, error) {
	return nil, errors.New("connect refused")
}

func Test_Cmd_Run_transport_error(t *testing.T) {
	err := Cmd{Tools: []string{"ping"}, transport: failingTransport{}}.Run(context.Background())
	require.ErrorContains(t, err, "serve stdio: connect refused")
}

func Test_Cmd_Run_transport_error_after_cancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Cmd{Tools: []string{"ping"}, transport: failingTransport{}}.Run(ctx)
	require.ErrorContains(t, err, "serve stdio: connect refused")
}

func Test_Cmd_Run_stdio_eof(t *testing.T) {
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	savedStdin := os.Stdin
	os.Stdin = reader
	defer func() {
		os.Stdin = savedStdin
		_ = reader.Close()
	}()

	require.NoError(t, Cmd{Tools: []string{"ping"}}.Run(context.Background()))
}
