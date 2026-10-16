package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/chat"
	"github.com/hangxie/chatops/internal/mcp"
)

// scrape returns the exposition text the handler serves.
func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body, err := io.ReadAll(rec.Body)
	require.NoError(t, err)
	return string(body)
}

func requireLines(t *testing.T, text string, lines ...string) {
	t.Helper()
	got := strings.Split(text, "\n")
	for _, line := range lines {
		require.Contains(t, got, line)
	}
}

func Test_New(t *testing.T) {
	text := scrape(t, New("v1.2.3"))
	requireLines(t, text, `chatops_build_info{version="v1.2.3"} 1`)
	require.Contains(t, text, "go_goroutines ")
	require.Contains(t, text, "process_")
}

func Test_Metrics_Watch(t *testing.T) {
	m := New("v0")
	m.Watch(Sources{
		Pending:  func() int { return 3 },
		InFlight: func() int { return 2 },
		Chat: func() []chat.BackendHealth {
			return []chat.BackendHealth{{Name: "slack", Up: true}, {Name: "console"}}
		},
		MCP: func() []mcp.ServerHealth {
			return []mcp.ServerHealth{{ID: "kubernetes", Up: true, Tools: 34}, {ID: "status", Tools: 1}}
		},
	})
	requireLines(t, scrape(t, m),
		`chatops_questions_pending 3`,
		`chatops_turns_in_flight 2`,
		`chatops_chat_backend_up{backend="slack"} 1`,
		`chatops_chat_backend_up{backend="console"} 0`,
		`chatops_mcp_server_up{server="kubernetes"} 1`,
		`chatops_mcp_server_up{server="status"} 0`,
		`chatops_mcp_tools{server="kubernetes"} 34`,
		`chatops_mcp_tools{server="status"} 1`,
	)
}
