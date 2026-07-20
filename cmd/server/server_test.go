package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alecthomas/kong"
	"github.com/stretchr/testify/require"

	chatslack "github.com/hangxie/chatops/chat/slack"
	"github.com/hangxie/chatops/tool"
)

func Test_Cmd_parse(t *testing.T) {
	testCases := map[string]struct {
		args    []string
		errMsg  string
		command Cmd
	}{
		"all-options": {
			args: []string{
				"--chat", "telnet://localhost:6023",
				"--planner", "ping://",
				"--credentials", "json-file:///etc/chatops/credentials.json",
				"--connection-id", "operations",
				"--max-concurrency", "3",
			},
			command: Cmd{
				ChatURL:        "telnet://localhost:6023",
				PlannerURL:     "ping://",
				CredentialsURL: "json-file:///etc/chatops/credentials.json",
				ConnectionID:   "operations",
				MaxConcurrency: 3,
			},
		},
		"required-chat": {
			args:   []string{"--planner", "ping://"},
			errMsg: "--chat",
		},
		"required-planner": {
			args:   []string{"--chat", "telnet://localhost:6023"},
			errMsg: "--planner",
		},
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
			require.Equal(t, tc.command, command)
		})
	}
}

func Test_run_ping_round_trip_and_graceful_cancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() {
		require.NoError(t, listener.Close())
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	command := Cmd{ChatURL: fmt.Sprintf("telnet://%s", listener.Addr()), PlannerURL: "ping://", ConnectionID: "test"}
	go func() {
		result <- command.Run(ctx)
	}()

	conn, err := listener.Accept()
	require.NoError(t, err)
	defer func() {
		require.NoError(t, conn.Close())
	}()
	require.NoError(t, conn.SetDeadline(time.Now().Add(3*time.Second)))
	reader := bufio.NewReader(conn)
	_, err = fmt.Fprintln(conn, "please ping it")
	require.NoError(t, err)
	reply, err := reader.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "do you want me to ping? (yes/no)\n", reply)

	_, err = fmt.Fprintln(conn, "yes")
	require.NoError(t, err)
	reply, err = reader.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "pong\n", reply)
	cancel()
	require.NoError(t, <-result)
}

func Test_run_closes_chat_when_planner_open_fails(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { require.NoError(t, listener.Close()) }()

	result := make(chan error, 1)
	command := Cmd{ChatURL: fmt.Sprintf("telnet://%s", listener.Addr()), PlannerURL: "unknown://"}
	go func() { result <- command.run(context.Background()) }()
	conn, err := listener.Accept()
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()

	require.ErrorContains(t, <-result, "open planner")
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
	_, err = bufio.NewReader(conn).ReadByte()
	require.ErrorIs(t, err, io.EOF)
}

func Test_run_reports_engine_failure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { require.NoError(t, listener.Close()) }()

	result := make(chan error, 1)
	command := Cmd{ChatURL: fmt.Sprintf("telnet://%s", listener.Addr()), PlannerURL: "ping://"}
	go func() { result <- command.run(context.Background()) }()
	conn, err := listener.Accept()
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	require.ErrorContains(t, <-result, "run engine")
}

func Test_run_opens_and_closes_credentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	require.NoError(t, os.WriteFile(path, []byte(`{}`), 0o600))
	command := Cmd{CredentialsURL: "json-file://" + path, ChatURL: "unknown://", PlannerURL: "ping://"}
	require.ErrorContains(t, command.run(context.Background()), "open chat")
}

func Test_toolRegistry_opens_status_tool(t *testing.T) {
	tl, err := toolRegistry().Open(context.Background(), "status://", nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, tl.Close()) }()

	result, err := tl.Invoke(context.Background(), tool.Call{Action: "list"})
	require.NoError(t, err)
	require.Contains(t, result.Text, "github")
	require.Contains(t, result.Text, "docker-hub")
}

func Test_run_reports_open_errors(t *testing.T) {
	testCases := map[string]struct {
		command Cmd
		errMsg  string
	}{
		"credentials": {
			command: Cmd{CredentialsURL: "unknown://", ChatURL: "telnet://localhost", PlannerURL: "ping://"},
			errMsg:  "open credentials",
		},
		"chat": {
			command: Cmd{ChatURL: "unknown://", PlannerURL: "ping://"},
			errMsg:  "open chat",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			err := tc.command.run(context.Background())
			require.ErrorContains(t, err, tc.errMsg)
		})
	}
}

func Test_chatRegistry_supports_slack(t *testing.T) {
	t.Setenv(chatslack.BotTokenEnv, "")
	t.Setenv(chatslack.AppTokenEnv, "")
	_, err := chatRegistry().Open(context.Background(), "slack://")
	require.ErrorContains(t, err, chatslack.BotTokenEnv)
}

type closer struct{ err error }

func (c *closer) Close() error { return c.err }

func Test_close_helpers(t *testing.T) {
	testErr := errors.New("close failed")
	testCases := map[string]struct {
		close func() error
		errIs error
	}{
		"success": {close: func() error { return closeNamed("component", &closer{}) }},
		"error":   {close: func() error { return closeNamed("component", &closer{err: testErr}) }, errIs: testErr},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			err := tc.close()
			if tc.errIs == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tc.errIs)
		})
	}
}
