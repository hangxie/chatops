package testutils_test

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/testutils"
)

type echoArgs struct {
	Text string `json:"text"`
}

// newEchoServer serves one tool echoing its argument back as text.
func newEchoServer(t *testing.T) *mcp.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "echo", Version: "v0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "Echo the text."},
		func(_ context.Context, _ *mcp.CallToolRequest, args echoArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: args.Text}}}, nil, nil
		})
	return srv
}

func Test_MCPSession_round_trip(t *testing.T) {
	session := testutils.MCPSession(t, newEchoServer(t))

	require.Equal(t, []string{"echo"}, testutils.ToolNames(t, session))

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "echo",
		Arguments: map[string]any{"text": "hi"},
	})
	require.NoError(t, err)
	require.Equal(t, "hi", testutils.ResultText(t, result))
}

func Test_ResultText(t *testing.T) {
	testCases := map[string]struct {
		result *mcp.CallToolResult
		want   string
	}{
		"empty": {result: &mcp.CallToolResult{}, want: ""},
		"single": {
			result: &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "one"}}},
			want:   "one",
		},
		"joined": {
			result: &mcp.CallToolResult{Content: []mcp.Content{
				&mcp.TextContent{Text: "one"},
				&mcp.TextContent{Text: "two"},
			}},
			want: "one\ntwo",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, testutils.ResultText(t, tc.result))
		})
	}
}
