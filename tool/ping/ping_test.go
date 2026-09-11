package ping

import (
	"context"
	"net/url"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/testutils"
)

// newServer registers the ping group on a fresh server.
func newServer(t *testing.T, opts url.Values) *mcp.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	require.NoError(t, Register(srv, nil, opts))
	return srv
}

func Test_Register_options(t *testing.T) {
	testCases := map[string]struct {
		opts   url.Values
		errMsg string
	}{
		"none":    {opts: nil},
		"empty":   {opts: url.Values{}},
		"unknown": {opts: url.Values{"region": {"x"}}, errMsg: "takes no options"},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
			err := Register(srv, nil, tc.opts)
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
		})
	}
}

func Test_Register_declares_tool(t *testing.T) {
	session := testutils.MCPSession(t, newServer(t, nil))

	listed, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, listed.Tools, 1)
	require.Equal(t, ToolName, listed.Tools[0].Name)
	require.NotEmpty(t, listed.Tools[0].Description)
	require.NotNil(t, listed.Tools[0].InputSchema)
}

func Test_Call(t *testing.T) {
	session := testutils.MCPSession(t, newServer(t, nil))

	// The tool always answers "pong" and reads no arguments.
	testCases := map[string]map[string]any{
		"no-arguments":    nil,
		"empty-arguments": {},
	}

	for name, args := range testCases {
		t.Run(name, func(t *testing.T) {
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
				Name:      ToolName,
				Arguments: args,
			})
			require.NoError(t, err)
			require.False(t, result.IsError)
			require.Equal(t, "pong", testutils.ResultText(t, result))
		})
	}
}

func Test_Call_cancelled_context(t *testing.T) {
	session := testutils.MCPSession(t, newServer(t, nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: ToolName})
	require.Error(t, err)
}

func Test_handle_honors_cancellation(t *testing.T) {
	// The handler guards its own context, so a call cancelled after it has
	// been dispatched stops rather than answering.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := handle(ctx, nil, Args{})
	require.ErrorIs(t, err, context.Canceled)
}

func Test_Call_unknown_tool(t *testing.T) {
	session := testutils.MCPSession(t, newServer(t, nil))

	_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "nope"})
	require.ErrorContains(t, err, "nope")
}
