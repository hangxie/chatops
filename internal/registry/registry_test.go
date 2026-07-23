package registry_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/registry"
	"github.com/hangxie/chatops/tool"
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
	p, err := registry.Planner().Open(context.Background(), "openai-chat-completions://api.openai.com/v1?model=gpt-5&keyless=true", nil, registry.Tool())
	require.NoError(t, err)
	require.NotNil(t, p)
	require.NoError(t, p.Close())
}

func Test_Tool_opens_status_tools(t *testing.T) {
	require.Equal(t, []string{"ping", "status-check", "status-list"}, registry.Tool().Schemes())

	tl, err := registry.Tool().Open(context.Background(), "status-list://", nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, tl.Close()) }()

	result, err := tl.Invoke(context.Background(), tool.Call{})
	require.NoError(t, err)
	require.Contains(t, result.Text, "github")
	require.Contains(t, result.Text, "docker-hub")
}
