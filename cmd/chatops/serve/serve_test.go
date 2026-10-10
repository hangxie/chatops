package serve

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/config"
	"github.com/hangxie/chatops/internal/conversation"
	"github.com/hangxie/chatops/pkg/mcpserver/ping"
)

// stdioServerEnv makes the test binary act as a ping MCP server over stdio.
const stdioServerEnv = "CHATOPS_SERVE_TEST_STDIO_SERVER"

func TestMain(m *testing.M) {
	if os.Getenv(stdioServerEnv) == "1" {
		server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "stdio-test", Version: "v0"}, nil)
		ping.Register(server)
		if err := server.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// scriptedLLM calls builtin__ping on the first request and answers on the second.
func scriptedLLM(t *testing.T) *httptest.Server {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1)%2 == 1 {
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"builtin__ping","arguments":"{}"}}]}}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"The server replied pong."}}]}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func writeConfig(t *testing.T, llmURL string, slack bool) string {
	t.Helper()
	chat := ""
	if slack {
		chat = "chat:\n  slack:\n    bot_token_env: CHATOPS_SERVE_TEST_BOT\n    app_token_env: CHATOPS_SERVE_TEST_APP\n"
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := fmt.Sprintf(`%sllm:
  base_url: %s/v1
  model: test-model
agent:
  system_prompt: test prompt
mcp:
  servers:
    builtin:
      transport: stdio
      command: %s
      args: ["-test.run=^$"]
      env:
        %s: "1"
`, chat, llmURL, os.Args[0], stdioServerEnv)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// fakeReceiver submits one message, waits for its reply, then stops like a cancelled adapter.
type fakeReceiver struct {
	reply chan string
	err   error
}

func (r *fakeReceiver) Run(ctx context.Context, submit func(conversation.Message) error) error {
	err := submit(conversation.Message{Conversation: "slack:T1:C1:1.1", User: "U1", Text: "is the builtin server up?", Reply: func(_ context.Context, text string) error {
		r.reply <- text
		return nil
	}})
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
		return errors.New("no reply")
	case text := <-r.reply:
		r.reply <- text
	}
	return r.err
}

func fakeSlack(r *fakeReceiver) func(config.Slack, *slog.Logger) (receiver, error) {
	return func(config.Slack, *slog.Logger) (receiver, error) { return r, nil }
}

func Test_Cmd_Run_end_to_end(t *testing.T) {
	var logs bytes.Buffer
	r := &fakeReceiver{reply: make(chan string, 1)}
	cmd := Cmd{Config: writeConfig(t, scriptedLLM(t).URL, true), LogLevel: "info", LogFormat: "json", errOut: &logs, newReceiver: fakeSlack(r)}

	require.NoError(t, cmd.Run(context.Background()))
	require.Equal(t, "The server replied pong.", <-r.reply)
	require.Contains(t, logs.String(), `"msg":"chatops serving"`)
	require.Contains(t, logs.String(), `"tools":1`)
	require.Contains(t, logs.String(), `"level":"WARN","msg":"authorization is not enabled: every user who can message the bot can call every tool"`)
}

func Test_Cmd_Run_receiver_error(t *testing.T) {
	r := &fakeReceiver{reply: make(chan string, 1), err: errors.New("slack: socket mode: invalid_auth")}
	cmd := Cmd{Config: writeConfig(t, scriptedLLM(t).URL, true), LogLevel: "warn", LogFormat: "text", errOut: io.Discard, newReceiver: fakeSlack(r)}
	require.EqualError(t, cmd.Run(context.Background()), "slack: socket mode: invalid_auth")
}

func Test_Cmd_Run_errors(t *testing.T) {
	llmURL := scriptedLLM(t).URL
	tests := map[string]struct {
		cmd    Cmd
		errMsg string
	}{
		"bad-level":     {cmd: Cmd{Config: writeConfig(t, llmURL, true), LogLevel: "loud", LogFormat: "text"}, errMsg: `log level: slog: level string "loud": unknown name`},
		"bad-format":    {cmd: Cmd{Config: writeConfig(t, llmURL, true), LogLevel: "info", LogFormat: "xml"}, errMsg: `log format: must be "text" or "json", got "xml"`},
		"missing-file":  {cmd: Cmd{Config: filepath.Join(t.TempDir(), "none.yaml"), LogLevel: "info", LogFormat: "text"}, errMsg: "read config"},
		"no-slack":      {cmd: Cmd{Config: writeConfig(t, llmURL, false), LogLevel: "info", LogFormat: "text"}, errMsg: "serve requires chat.slack in the config"},
		"missing-token": {cmd: Cmd{Config: writeConfig(t, llmURL, true), LogLevel: "info", LogFormat: "text"}, errMsg: "slack bot token: environment variable CHATOPS_SERVE_TEST_BOT is not set"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			tc.cmd.errOut = io.Discard
			require.ErrorContains(t, tc.cmd.Run(context.Background()), tc.errMsg)
		})
	}
}

func Test_Cmd_Run_dependency_errors(t *testing.T) {
	const slackSection = "chat:\n  slack:\n    bot_token_env: B\n    app_token_env: A\n"
	tests := map[string]struct {
		config string
		errMsg string
	}{
		"bad-llm": {config: slackSection + "llm:\n  base_url: http://localhost/v1?x=1\n  model: m\nagent:\n  system_prompt: p\n", errMsg: "must not carry query"},
		"bad-mcp": {
			config: slackSection + "llm:\n  base_url: http://localhost/v1\n  model: m\nagent:\n  system_prompt: p\nmcp:\n  servers:\n    x:\n      transport: stdio\n      command: /nonexistent/mcp\n",
			errMsg: `mcp server "x": connect`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			require.NoError(t, os.WriteFile(path, []byte(tc.config), 0o600))
			cmd := Cmd{Config: path, LogLevel: "info", LogFormat: "text", errOut: io.Discard, newReceiver: fakeSlack(&fakeReceiver{})}
			require.ErrorContains(t, cmd.Run(context.Background()), tc.errMsg)
		})
	}
}

func Test_Cmd_defaults(t *testing.T) {
	cmd := Cmd{}
	require.Equal(t, os.Stderr, cmd.logOutput())
	t.Setenv("CHATOPS_SERVE_TEST_BOT", "xoxb-1")
	t.Setenv("CHATOPS_SERVE_TEST_APP", "xapp-1")
	r, err := cmd.receiverFactory()(config.Slack{BotTokenEnv: "CHATOPS_SERVE_TEST_BOT", AppTokenEnv: "CHATOPS_SERVE_TEST_APP"}, slog.Default())
	require.NoError(t, err)
	require.NotNil(t, r)
	require.True(t, strings.HasPrefix(fmt.Sprintf("%T", r), "*slack."))
}
