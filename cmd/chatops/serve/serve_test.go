package serve

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/chat"
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
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		if requests%2 == 1 {
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"builtin__ping","arguments":"{}"}}]}}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"The server replied pong."}}]}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func writeConfig(t *testing.T, llmURL string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := fmt.Sprintf(`chat:
  slack:
    type: slack
    bot_token_env: CHATOPS_SERVE_TEST_BOT
    app_token_env: CHATOPS_SERVE_TEST_APP
llm:
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
`, llmURL, os.Args[0], stdioServerEnv)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// fakeBackend submits one message, waits for its reply, then stops like a cancelled backend.
type fakeBackend struct {
	reply chan string
	err   error
}

func (b *fakeBackend) Run(ctx context.Context, submit func(conversation.Message) error) error {
	err := submit(conversation.Message{Conversation: "C1", User: "U1", Text: "is the builtin server up?", Reply: func(_ context.Context, reply conversation.Reply) error {
		b.reply <- reply.Plain()
		return nil
	}})
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
		return errors.New("no reply")
	case text := <-b.reply:
		b.reply <- text
	}
	return b.err
}

func (b *fakeBackend) Connected() bool { return true }

func injected(b chat.Backend) func(config.Chat, []string, *slog.Logger) ([]chat.Named, error) {
	return func(config.Chat, []string, *slog.Logger) ([]chat.Named, error) {
		return []chat.Named{{Name: "fake", Backend: b}}, nil
	}
}

func Test_Cmd_Run_end_to_end(t *testing.T) {
	var logs bytes.Buffer
	b := &fakeBackend{reply: make(chan string, 1)}
	cmd := Cmd{Config: writeConfig(t, scriptedLLM(t).URL), Chat: []string{"slack"}, LogLevel: "info", LogFormat: "json", errOut: &logs, selectBackends: injected(b)}

	require.NoError(t, cmd.Run(context.Background()))
	require.Equal(t, "The server replied pong.", <-b.reply)
	require.Contains(t, logs.String(), `"msg":"chatops serving"`)
	require.Contains(t, logs.String(), `"tools":1`)
	require.Contains(t, logs.String(), `"level":"WARN","msg":"authorization is not enabled: every user who can message the bot can call every tool"`)
}

// scrapeBackend asks one question, scrapes /metrics until the delivered reply is counted, then probes /readyz.
type scrapeBackend struct {
	addr  chan string
	body  string
	ready int
}

func (b *scrapeBackend) Run(ctx context.Context, submit func(conversation.Message) error) error {
	replied := make(chan struct{})
	err := submit(conversation.Message{Conversation: "C1", User: "U1", Text: "ping", Reply: func(context.Context, conversation.Reply) error {
		close(replied)
		return nil
	}})
	if err != nil {
		return err
	}
	<-replied
	base := "http://" + <-b.addr
	url := base + "/metrics"
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		resp, err := http.Get(url)
		if err != nil {
			return err
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return err
		}
		if b.body = string(body); strings.Contains(b.body, "chatops_response_duration_seconds_count") {
			return b.probeReady(base + "/readyz")
		}
	}
	return errors.New("delivered reply never counted")
}

func (b *scrapeBackend) Connected() bool { return true }

func (b *scrapeBackend) probeReady(url string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	b.ready = resp.StatusCode
	_, err = io.Copy(io.Discard, resp.Body)
	return errors.Join(err, resp.Body.Close())
}

func Test_Cmd_Run_metrics(t *testing.T) {
	path := writeConfig(t, scriptedLLM(t).URL)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString("metrics:\n  listen: 127.0.0.1:0\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	b := &scrapeBackend{addr: make(chan string, 1)}
	var logs bytes.Buffer
	cmd := Cmd{
		Config: path, Chat: []string{"slack"}, LogLevel: "info", LogFormat: "json", errOut: &logs, selectBackends: injected(b),
		listen: func(network, address string) (net.Listener, error) {
			ln, err := net.Listen(network, address)
			if err == nil {
				b.addr <- ln.Addr().String()
			}
			return ln, err
		},
	}

	require.NoError(t, cmd.Run(context.Background()))
	for _, line := range []string{
		`chatops_questions_received_total{backend="fake"} 1`,
		`chatops_response_duration_seconds_count{backend="fake",outcome="answered"} 1`,
		`chatops_tool_call_duration_seconds_count{outcome="ok",server="builtin",tool="ping"} 1`,
		`chatops_mcp_server_up{server="builtin"} 1`,
		`chatops_mcp_tools{server="builtin"} 1`,
		`chatops_build_info{version="(devel)"} 1`,
	} {
		require.Contains(t, strings.Split(b.body, "\n"), line)
	}
	require.Equal(t, http.StatusOK, b.ready)
	require.Contains(t, logs.String(), `"msg":"metrics serving","address":"127.0.0.1:`)
}

// closingBackend answers one question, kills the metrics listener, then runs until cancelled.
type closingBackend struct {
	listener chan net.Listener
}

func (b *closingBackend) Run(ctx context.Context, submit func(conversation.Message) error) error {
	replied := make(chan struct{})
	err := submit(conversation.Message{Conversation: "C1", User: "U1", Text: "ping", Reply: func(context.Context, conversation.Reply) error {
		close(replied)
		return nil
	}})
	if err != nil {
		return err
	}
	<-replied
	if err := (<-b.listener).Close(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return nil
	case <-time.After(10 * time.Second):
		return errors.New("serve kept running after the metrics server failed")
	}
}

func (b *closingBackend) Connected() bool { return true }

func Test_Cmd_Run_metrics_server_failure(t *testing.T) {
	path := writeConfig(t, scriptedLLM(t).URL)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(data, []byte("metrics:\n  listen: 127.0.0.1:0\n")...), 0o600))

	b := &closingBackend{listener: make(chan net.Listener, 1)}
	cmd := Cmd{
		Config: path, Chat: []string{"slack"}, LogLevel: "info", LogFormat: "text", errOut: io.Discard, selectBackends: injected(b),
		listen: func(network, address string) (net.Listener, error) {
			ln, err := net.Listen(network, address)
			if err == nil {
				b.listener <- ln
			}
			return ln, err
		},
	}
	require.ErrorContains(t, cmd.Run(context.Background()), "metrics server stopped: ")
}

func Test_Cmd_Run_metrics_listen_error(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = busy.Close() })
	path := writeConfig(t, scriptedLLM(t).URL)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(data, []byte("metrics:\n  listen: "+busy.Addr().String()+"\n")...), 0o600))

	cmd := Cmd{Config: path, Chat: []string{"slack"}, LogLevel: "info", LogFormat: "text", errOut: io.Discard, selectBackends: injected(&fakeBackend{reply: make(chan string, 1)})}
	require.ErrorContains(t, cmd.Run(context.Background()), "metrics: listen tcp "+busy.Addr().String())
}

func Test_Cmd_Run_backend_error(t *testing.T) {
	b := &fakeBackend{reply: make(chan string, 1), err: errors.New("slack: socket mode: invalid_auth")}
	cmd := Cmd{Config: writeConfig(t, scriptedLLM(t).URL), Chat: []string{"slack"}, LogLevel: "warn", LogFormat: "text", errOut: io.Discard, selectBackends: injected(b)}
	require.EqualError(t, cmd.Run(context.Background()), "slack: socket mode: invalid_auth")
}

func Test_Cmd_Run_errors(t *testing.T) {
	llmURL := scriptedLLM(t).URL
	tests := map[string]struct {
		cmd    Cmd
		errMsg string
	}{
		"bad-level":       {cmd: Cmd{Config: writeConfig(t, llmURL), Chat: []string{"slack"}, LogLevel: "loud", LogFormat: "text"}, errMsg: `log level: slog: level string "loud": unknown name`},
		"bad-format":      {cmd: Cmd{Config: writeConfig(t, llmURL), Chat: []string{"slack"}, LogLevel: "info", LogFormat: "xml"}, errMsg: `log format: must be "text" or "json", got "xml"`},
		"missing-file":    {cmd: Cmd{Config: filepath.Join(t.TempDir(), "none.yaml"), Chat: []string{"slack"}, LogLevel: "info", LogFormat: "text"}, errMsg: "read config"},
		"unknown-backend": {cmd: Cmd{Config: writeConfig(t, llmURL), Chat: []string{"ghost"}, LogLevel: "info", LogFormat: "text"}, errMsg: `chat backend "ghost" is not defined in the config`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			tc.cmd.errOut = io.Discard
			require.ErrorContains(t, tc.cmd.Run(context.Background()), tc.errMsg)
		})
	}
}

func Test_Cmd_Run_dependency_errors(t *testing.T) {
	const chatSection = "chat:\n  slack:\n    type: slack\n    bot_token_env: B\n    app_token_env: A\n"
	tests := map[string]struct {
		config string
		errMsg string
	}{
		"bad-llm": {config: chatSection + "llm:\n  base_url: http://localhost/v1?x=1\n  model: m\nagent:\n  system_prompt: p\n", errMsg: "must not carry query"},
		"bad-mcp": {
			config: chatSection + "llm:\n  base_url: http://localhost/v1\n  model: m\nagent:\n  system_prompt: p\nmcp:\n  servers:\n    x:\n      transport: stdio\n      command: /nonexistent/mcp\n",
			errMsg: `mcp server "x": connect`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			require.NoError(t, os.WriteFile(path, []byte(tc.config), 0o600))
			cmd := Cmd{Config: path, Chat: []string{"slack"}, LogLevel: "info", LogFormat: "text", errOut: io.Discard, selectBackends: injected(&fakeBackend{reply: make(chan string, 1)})}
			require.ErrorContains(t, cmd.Run(context.Background()), tc.errMsg)
		})
	}
}

func Test_Cmd_selector_default(t *testing.T) {
	t.Setenv("CHATOPS_SERVE_TEST_BOT", "xoxb-1")
	t.Setenv("CHATOPS_SERVE_TEST_APP", "xapp-1")
	cfg := config.Chat{"slack": {Type: config.ChatSlack, BotTokenEnv: "CHATOPS_SERVE_TEST_BOT", AppTokenEnv: "CHATOPS_SERVE_TEST_APP"}}
	backends, err := Cmd{}.selector()(cfg, []string{"slack"}, slog.Default())
	require.NoError(t, err)
	require.Len(t, backends, 1)
	require.Equal(t, "slack", backends[0].Name)
}

func Test_Cmd_defaults(t *testing.T) {
	require.Equal(t, os.Stderr, Cmd{}.logOutput())
}
