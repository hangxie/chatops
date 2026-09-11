package mcpserve_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/testutils"
	"github.com/hangxie/chatops/mcpserve"
)

// registerPanicking is a RegisterFunc whose tool always panics.
func registerPanicking(s *mcp.Server, _ mcpserve.Options) error {
	mcp.AddTool(s, &mcp.Tool{Name: "boom", Description: "Always panics."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			panic("kaboom")
		})
	return nil
}

func Test_Server_contains_panicking_tool(t *testing.T) {
	var logs bytes.Buffer
	reg := mcpserve.NewRegistry(
		mcpserve.Group{Name: "boom", Register: registerPanicking},
	)
	srv, err := reg.Server("boom", mcpserve.Options{Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	require.NoError(t, err)
	session := testutils.MCPSession(t, srv)

	// A panicking tool must cost its own call and nothing more: the SDK does
	// not recover handler panics, so without containment it would take the
	// process down.
	_, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "boom"})
	require.ErrorContains(t, err, `tool "boom" panicked`)

	// The detail is logged, not returned, so it cannot reach chat — and the
	// record names the tool, since the method alone would not say which one
	// misbehaved.
	require.Contains(t, logs.String(), "handler panicked")
	require.Contains(t, logs.String(), "kaboom")
	require.Contains(t, logs.String(), "group=boom")
	require.Contains(t, logs.String(), `handler="tool \"boom\""`)

	// The server is still serving afterwards.
	require.Equal(t, []string{"boom"}, testutils.ToolNames(t, session))
}

func Test_RecoverMiddleware_passes_through_success(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "ok", Description: "Succeeds."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "fine"}}}, nil, nil
		})
	srv.AddReceivingMiddleware(mcpserve.RecoverMiddleware("test", nil))
	session := testutils.MCPSession(t, srv)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "ok"})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Equal(t, "fine", testutils.ResultText(t, result))
}

func Test_RecoverMiddleware_names_a_non_tool_call(t *testing.T) {
	// Receiving middleware wraps every method, not only tools/call, so a
	// request that names no tool is reported by its method rather than as an
	// anonymous tool.
	var logs bytes.Buffer
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/list" {
				panic("listing boom")
			}
			return next(ctx, method, req)
		}
	})
	// Added last, as Registry.Server does, so the guard is outermost and
	// contains the middleware above it as well as the handlers.
	srv.AddReceivingMiddleware(mcpserve.RecoverMiddleware("grp", slog.New(slog.NewTextHandler(&logs, nil))))
	session := testutils.MCPSession(t, srv)

	_, err := session.ListTools(context.Background(), nil)
	require.ErrorContains(t, err, "tools/list panicked")
	require.Contains(t, logs.String(), "handler=tools/list")
	require.Contains(t, logs.String(), "method=tools/list")
}

func Test_RecoverMiddleware_nil_logger(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "boom", Description: "Panics."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			panic("kaboom")
		})
	srv.AddReceivingMiddleware(mcpserve.RecoverMiddleware("test", nil))
	session := testutils.MCPSession(t, srv)

	// A nil logger discards the detail; the panic is still contained.
	_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "boom"})
	require.ErrorContains(t, err, "panicked")
}
