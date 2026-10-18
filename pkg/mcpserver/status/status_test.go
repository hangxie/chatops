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
	require.Contains(t, tool.Description, "a service or application")
	require.Contains(t, tool.Description, "for a self-hosted one, its health endpoint")
	require.True(t, tool.Annotations.ReadOnlyHint)
	require.True(t, tool.Annotations.IdempotentHint)
	require.True(t, *tool.Annotations.OpenWorldHint)
	schema, ok := tool.InputSchema.(map[string]any)
	require.True(t, ok)
	require.Contains(t, tool.Description, `Use services ["all"] to check every service`)
	require.Equal(t, []any{"services"}, schema["required"])
	services := schema["properties"].(map[string]any)["services"].(map[string]any)
	require.Equal(t, "array", services["type"])
	require.EqualValues(t, 1, services["minItems"])
	require.Equal(t, []any{"all", "github", "anthropic", "cloudflare", "openai", "openrouter", "gemini", "google-workspace", "slack", "docker-hub"}, services["items"].(map[string]any)["enum"])
	require.Contains(t, services["description"], "anthropic (Claude, claude.ai, Claude Console, the Claude API, Claude Code, Claude Cowork)")
	require.Contains(t, services["description"], "openrouter (OpenRouter, openrouter.ai, the OpenRouter API, OpenRouter activity logs and usage)")
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
	services := result.Tools[0].InputSchema.(map[string]any)["properties"].(map[string]any)["services"].(map[string]any)
	require.Equal(t, `The services to check, or ["all"] for every one: plain.`, services["description"])
}

func Test_service_status_call(t *testing.T) {
	tests := map[string]struct {
		arguments map[string]any
		text      string
		services  int
		errMsg    string
	}{
		"one":        {arguments: map[string]any{"services": []any{"alpha"}}, text: "[OK] Alpha - fine", services: 1},
		"two":        {arguments: map[string]any{"services": []any{"gamma", "alpha"}}, text: "[DEGRADED] Gamma - slow\n  API latency\n[OK] Alpha - fine", services: 2},
		"all":        {arguments: map[string]any{"services": []any{"all"}}, text: "[OK] Alpha - fine\n[UNKNOWN] Beta - unable to check: HTTP 503\n[DEGRADED] Gamma - slow\n  API latency", services: 3},
		"unreadable": {arguments: map[string]any{"services": []any{"beta"}}, errMsg: "unable to check Beta: HTTP 503"},
		"unknown":    {arguments: map[string]any{"services": []any{"github"}}, errMsg: "enum"},
		"empty":      {arguments: map[string]any{"services": []any{}}, errMsg: "minItems"},
		"string":     {arguments: map[string]any{"services": "alpha"}, errMsg: "array"},
		"missing":    {arguments: map[string]any{}, errMsg: "services"},
		"extra":      {arguments: map[string]any{"services": []any{"alpha"}, "region": "us"}, errMsg: `unexpected additional properties ["region"]`},
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
	_, _, err := fakeChecker().handle(ctx, nil, Input{Services: []string{"all"}})
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
