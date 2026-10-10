package status

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func connect(t *testing.T, register func(*mcp.Server)) *mcp.ClientSession {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	register(server)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "v0"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func Test_Register_lists_service_status(t *testing.T) {
	cfg, err := Load(packagedConfig)
	require.NoError(t, err)
	session := connect(t, func(server *mcp.Server) { require.NoError(t, Register(server, cfg)) })

	result, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, result.Tools, 1)

	tool := result.Tools[0]
	require.Equal(t, "service_status", tool.Name)
	require.Contains(t, tool.Description, "Only the listed services are covered")
	require.True(t, tool.Annotations.ReadOnlyHint)
	require.True(t, tool.Annotations.IdempotentHint)
	require.True(t, *tool.Annotations.OpenWorldHint)
	schema, ok := tool.InputSchema.(map[string]any)
	require.True(t, ok)
	require.Equal(t, []any{"service"}, schema["required"])
	service := schema["properties"].(map[string]any)["service"].(map[string]any)
	require.Equal(t, []any{"all", "github", "anthropic", "cloudflare", "openai", "gemini", "google-workspace", "slack", "docker-hub"}, service["enum"])
	require.Contains(t, service["description"], "anthropic (Claude, claude.ai, the Claude API, Claude Code)")
	require.Contains(t, service["description"], "openai (ChatGPT, Codex, the OpenAI API)")
	require.NotNil(t, tool.OutputSchema)
}

func Test_Register_invalid_config(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	require.EqualError(t, Register(server, Config{}), "at least one service is required")
}

func Test_register_service_without_covers(t *testing.T) {
	c := &checker{services: []service{{name: "plain", display: "Plain", check: fixed(Snapshot{}, nil)}}}
	session := connect(t, func(server *mcp.Server) { register(server, c) })
	result, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	service := result.Tools[0].InputSchema.(map[string]any)["properties"].(map[string]any)["service"].(map[string]any)
	require.Equal(t, `The service to check, or "all" for every one: plain.`, service["description"])
}

func Test_service_status_call(t *testing.T) {
	tests := map[string]struct {
		arguments map[string]any
		text      string
		services  int
		errMsg    string
	}{
		"one":     {arguments: map[string]any{"service": "alpha"}, text: "[OK] Alpha - fine", services: 1},
		"all":     {arguments: map[string]any{"service": "all"}, text: "[OK] Alpha - fine\n[UNKNOWN] Beta - unable to check: HTTP 503\n[DEGRADED] Gamma - slow\n  API latency", services: 3},
		"unknown": {arguments: map[string]any{"service": "github"}, errMsg: "enum"},
		"missing": {arguments: map[string]any{}, errMsg: "service"},
		"extra":   {arguments: map[string]any{"service": "alpha", "region": "us"}, errMsg: `unexpected additional properties ["region"]`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			session := connect(t, func(server *mcp.Server) { register(server, fakeChecker()) })

			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "service_status", Arguments: tc.arguments})
			require.NoError(t, err)
			require.Len(t, result.Content, 1)
			text, ok := result.Content[0].(*mcp.TextContent)
			require.True(t, ok)
			if tc.errMsg != "" {
				require.True(t, result.IsError)
				require.Contains(t, text.Text, tc.errMsg)
				return
			}
			require.False(t, result.IsError)
			require.Equal(t, tc.text, text.Text)
			structured, ok := result.StructuredContent.(map[string]any)
			require.True(t, ok)
			require.Len(t, structured["services"], tc.services)
		})
	}
}

func Test_handle_cancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := fakeChecker().handle(ctx, nil, Input{Service: "all"})
	require.ErrorIs(t, err, context.Canceled)
}

func Test_format(t *testing.T) {
	got := format([]Snapshot{
		{Service: "GitHub", Health: HealthPartialOutage, Summary: "Partial System Outage", Incidents: []Incident{
			{Name: "Actions delayed", Status: "investigating", URL: "https://stspg.io/x"},
			{Name: "Pages slow"},
		}},
		{Service: "Slack", Health: HealthMaintenance, Summary: "Scheduled maintenance"},
	})
	require.Equal(t, "[PARTIAL OUTAGE] GitHub - Partial System Outage\n  Actions delayed (investigating) https://stspg.io/x\n  Pages slow\n[MAINTENANCE] Slack - Scheduled maintenance", got)
}
