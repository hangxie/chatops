package registry_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hangxie/chatops/internal/registry"
	"github.com/hangxie/chatops/internal/testutils"
	"github.com/hangxie/chatops/mcpserve"
)

func Test_Chat_supports_registered_schemes(t *testing.T) {
	require.Equal(t, []string{"slack", "telnet"}, registry.Chat().Schemes())
}

func Test_Chat_supports_slack(t *testing.T) {
	_, err := registry.Chat().Open(context.Background(), "slack://", nil)
	require.ErrorContains(t, err, "credential store is not configured")
}

func Test_Credential_opens_jsonfile(t *testing.T) {
	_, err := registry.Credential().Open(context.Background(), "unknown://")
	require.ErrorContains(t, err, "unknown")
}

func Test_Planner_opens_ping(t *testing.T) {
	require.Equal(t, []string{"openai-chat-completions", "ping"}, registry.Planner().Schemes())

	p, err := registry.Planner().Open(context.Background(), "ping://", nil, nil)
	require.NoError(t, err)
	require.NotNil(t, p)
}

func Test_Planner_opens_openai(t *testing.T) {
	p, err := registry.Planner().Open(context.Background(), "openai-chat-completions://api.openai.com/v1?model=gpt-5&keyless=true", nil, nil)
	require.NoError(t, err)
	require.NotNil(t, p)
	require.NoError(t, p.Close())
}

func Test_Builtin_lists_groups(t *testing.T) {
	require.Equal(t, []string{"k8s", "ping", "status"}, registry.Builtin().Names())
}

func Test_Builtin_serves_status_tools(t *testing.T) {
	srv, err := registry.Builtin().Server("status", mcpserve.Options{})
	require.NoError(t, err)
	session := testutils.MCPSession(t, srv)

	require.Equal(t, []string{"status-check", "status-list"}, testutils.ToolNames(t, session))

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "status-list"})
	require.NoError(t, err)
	require.False(t, result.IsError)
	text := testutils.ResultText(t, result)
	require.Contains(t, text, "github")
	require.Contains(t, text, "docker-hub")
}

func Test_Builtin_unknown_group(t *testing.T) {
	_, err := registry.Builtin().Server("nope", mcpserve.Options{})
	require.ErrorContains(t, err, "unknown built-in group")
}
