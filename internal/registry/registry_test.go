package registry_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	chatslack "github.com/hangxie/chatops/chat/slack"
	"github.com/hangxie/chatops/internal/registry"
	"github.com/hangxie/chatops/tool"
)

func Test_Chat_supports_registered_schemes(t *testing.T) {
	require.Equal(t, []string{"slack", "telnet"}, registry.Chat().Schemes())
}

func Test_Chat_supports_slack(t *testing.T) {
	t.Setenv(chatslack.BotTokenEnv, "")
	t.Setenv(chatslack.AppTokenEnv, "")
	_, err := registry.Chat().Open(context.Background(), "slack://")
	require.ErrorContains(t, err, chatslack.BotTokenEnv)
}

func Test_Credential_opens_jsonfile(t *testing.T) {
	_, err := registry.Credential().Open(context.Background(), "unknown://")
	require.ErrorContains(t, err, "unknown")
}

func Test_Planner_opens_ping(t *testing.T) {
	require.Equal(t, []string{"ping"}, registry.Planner().Schemes())

	p, err := registry.Planner().Open(context.Background(), "ping://", nil, nil)
	require.NoError(t, err)
	require.NotNil(t, p)
}

func Test_Tool_opens_status_tool(t *testing.T) {
	require.Equal(t, []string{"ping", "status"}, registry.Tool().Schemes())

	tl, err := registry.Tool().Open(context.Background(), "status://", nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, tl.Close()) }()

	result, err := tl.Invoke(context.Background(), tool.Call{Action: "list"})
	require.NoError(t, err)
	require.Contains(t, result.Text, "github")
	require.Contains(t, result.Text, "docker-hub")
}
