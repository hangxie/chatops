package testutils

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// MCPSession runs srv on its own goroutine and returns a client session
// connected to it over an in-memory transport, closing both when the test
// ends.
//
// The transport is a real JSON-RPC round trip, so a tool exercised through it
// is exercised exactly as a remote one would be: arguments are marshalled,
// the server validates them against the declared schema, and the result comes
// back as wire JSON.
func MCPSession(t *testing.T, srv *mcp.Server) *mcp.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = srv.Run(ctx, serverTransport)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = session.Close()
		cancel()
		<-served
	})
	return session
}

// ResultText joins the text content of a tool result, which is what the
// engine would post into chat.
func ResultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	require.NotNil(t, result)
	parts := make([]string, 0, len(result.Content))
	for _, content := range result.Content {
		text, ok := content.(*mcp.TextContent)
		require.True(t, ok, "unexpected content type %T", content)
		parts = append(parts, text.Text)
	}
	return strings.Join(parts, "\n")
}

// ToolNames returns the names of the tools a session offers, in server order.
func ToolNames(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()
	listed, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	return names
}
