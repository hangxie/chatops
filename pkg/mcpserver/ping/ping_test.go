package ping

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	Register(server)

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

func Test_Register_lists_ping(t *testing.T) {
	session := connect(t)

	result, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, result.Tools, 1)

	tool := result.Tools[0]
	require.Equal(t, "ping", tool.Name)
	require.NotEmpty(t, tool.Description)
	require.NotNil(t, tool.Annotations)
	require.True(t, tool.Annotations.ReadOnlyHint)
	require.True(t, tool.Annotations.IdempotentHint)
	require.NotNil(t, tool.Annotations.OpenWorldHint)
	require.False(t, *tool.Annotations.OpenWorldHint)
}

func Test_ping_call(t *testing.T) {
	tests := map[string]struct {
		arguments any
	}{
		"no-arguments":    {arguments: nil},
		"empty-arguments": {arguments: map[string]any{}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			session := connect(t)

			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "ping", Arguments: tc.arguments})
			require.NoError(t, err)
			require.False(t, result.IsError)
			require.Len(t, result.Content, 1)
			text, ok := result.Content[0].(*mcp.TextContent)
			require.True(t, ok)
			require.Equal(t, "pong", text.Text)
			require.Equal(t, map[string]any{"reply": "pong"}, result.StructuredContent)
		})
	}
}

func Test_ping_rejects_unknown_arguments(t *testing.T) {
	session := connect(t)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "ping", Arguments: map[string]any{"target": "prod"}})
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	require.Contains(t, text.Text, `unexpected additional properties ["target"]`)
}
