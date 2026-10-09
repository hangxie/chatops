package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/pkg/mcpserver/ping"
)

// stdioServerEnv makes the test binary act as a ping MCP server over stdio.
const stdioServerEnv = "CHATOPS_CHAT_TEST_STDIO_SERVER"

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

// scriptedLLM asks for ping first, then answers quoting the tool result it received.
func scriptedLLM(t *testing.T) *httptest.Server {
	var mu sync.Mutex
	requests := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string  `json:"role"`
				Content *string `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		data, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(data, &body))

		mu.Lock()
		requests++
		n := requests
		mu.Unlock()

		if n == 1 {
			require.Len(t, body.Tools, 1)
			require.Equal(t, "builtin__ping", body.Tools[0].Function.Name)
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"builtin__ping","arguments":"{}"}}]}}]}`)
			return
		}
		last := body.Messages[len(body.Messages)-1]
		require.Equal(t, "tool", last.Role)
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":"The server replied %s."}}]}`, *last.Content)
	}))
}

func writeConfig(t *testing.T, llmURL string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := fmt.Sprintf(`llm:
  base_url: %s/v1
  model: test-model
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

func Test_Cmd_Run_end_to_end(t *testing.T) {
	llmServer := scriptedLLM(t)
	defer llmServer.Close()

	var out, errOut bytes.Buffer
	cmd := Cmd{
		Config:   writeConfig(t, llmServer.URL),
		LogLevel: "warn",
		in:       strings.NewReader("is the builtin server up?\n"),
		out:      &out,
		errOut:   &errOut,
	}
	require.NoError(t, cmd.Run(context.Background()))
	require.Equal(t, "> The server replied pong.\n> \n", out.String())
	require.Empty(t, errOut.String())
}

func Test_Cmd_Run_errors(t *testing.T) {
	dir := t.TempDir()
	badLLM := filepath.Join(dir, "bad-llm.yaml")
	require.NoError(t, os.WriteFile(badLLM, []byte("llm:\n  base_url: http://h/v1?x=1\n  model: m\n"), 0o600))
	badMCP := filepath.Join(dir, "bad-mcp.yaml")
	require.NoError(t, os.WriteFile(badMCP, []byte("llm:\n  base_url: http://h/v1\n  model: m\nmcp:\n  servers:\n    x:\n      transport: stdio\n      command: /nonexistent/mcp\n"), 0o600))

	tests := map[string]struct {
		config string
		level  string
		errMsg string
	}{
		"missing-config": {config: filepath.Join(dir, "none.yaml"), level: "warn", errMsg: "read config"},
		"bad-llm":        {config: badLLM, level: "warn", errMsg: "must not carry query"},
		"bad-mcp":        {config: badMCP, level: "warn", errMsg: `mcp server "x": connect`},
		"bad-level":      {config: badLLM, level: "loud", errMsg: `log level: slog: level string "loud": unknown name`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cmd := Cmd{Config: tc.config, LogLevel: tc.level, in: strings.NewReader(""), out: io.Discard, errOut: io.Discard}
			require.ErrorContains(t, cmd.Run(context.Background()), tc.errMsg)
		})
	}
}

func Test_Cmd_streams_default_to_stdio(t *testing.T) {
	in, out, errOut := Cmd{}.streams()
	require.Equal(t, os.Stdin, in)
	require.Equal(t, os.Stdout, out)
	require.Equal(t, os.Stderr, errOut)
}
