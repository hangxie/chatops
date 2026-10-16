package metrics

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hangxie/chatops/internal/chat"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/mcp"
)

func probe(t *testing.T, handler http.Handler) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	return rec.Code, rec.Body.String()
}

func Test_Metrics_Live(t *testing.T) {
	code, body := probe(t, New("v0").Live())
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "ok\n", body)
}

func Test_Metrics_Ready(t *testing.T) {
	up := []mcp.ServerHealth{{ID: "kubernetes", Up: true, Tools: 34}}
	tests := map[string]struct {
		sources *Sources
		code    int
		body    string
	}{
		"not-wired": {nil, http.StatusServiceUnavailable, "starting\n"},
		"ready": {
			&Sources{Accepting: func() bool { return true }, MCP: func() []mcp.ServerHealth { return up }},
			http.StatusOK, "ok\n",
		},
		"no-mcp-servers": {
			&Sources{Accepting: func() bool { return true }, MCP: func() []mcp.ServerHealth { return nil }},
			http.StatusOK, "ok\n",
		},
		"shutting-down": {
			&Sources{Accepting: func() bool { return false }, MCP: func() []mcp.ServerHealth { return up }},
			http.StatusServiceUnavailable, "shutting down\n",
		},
		"mcp-down": {
			&Sources{Accepting: func() bool { return true }, MCP: func() []mcp.ServerHealth {
				return []mcp.ServerHealth{{ID: "kubernetes", Up: true}, {ID: "status"}, {ID: "zz"}}
			}},
			http.StatusServiceUnavailable, "mcp server status: down\nmcp server zz: down\n",
		},
		"chat-down": {
			&Sources{Accepting: func() bool { return true }, MCP: func() []mcp.ServerHealth { return up }, Chat: func() []chat.BackendHealth {
				return []chat.BackendHealth{{Name: "console", Up: true}, {Name: "slack"}}
			}},
			http.StatusServiceUnavailable, "chat backend slack: not connected\n",
		},
		"all-failures": {
			&Sources{
				Accepting: func() bool { return false },
				Chat:      func() []chat.BackendHealth { return []chat.BackendHealth{{Name: "slack"}} },
				MCP:       func() []mcp.ServerHealth { return []mcp.ServerHealth{{ID: "status"}} },
			},
			http.StatusServiceUnavailable, "shutting down\nchat backend slack: not connected\nmcp server status: down\n",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			m := New("v0")
			if tc.sources != nil {
				tc.sources.Pending = func() int { return 0 }
				tc.sources.InFlight = func() int { return 0 }
				if tc.sources.Chat == nil {
					tc.sources.Chat = func() []chat.BackendHealth { return nil }
				}
				m.Watch(*tc.sources)
			}
			code, body := probe(t, m.Ready())
			require.Equal(t, tc.code, code)
			require.Equal(t, tc.body, body)
		})
	}
}
