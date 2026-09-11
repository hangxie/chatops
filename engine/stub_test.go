package engine

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/chat"
	"github.com/hangxie/chatops/mcphost"
	"github.com/hangxie/chatops/mcpserve"
	"github.com/hangxie/chatops/tool/reply"
)

// stubTool is one tool served by a stub MCP server: it records the arguments
// it was called with and answers with a fixed outcome.
type stubTool struct {
	name string

	mu       sync.Mutex
	calls    []map[string]any
	text     string
	toolErr  error
	panics   bool
	rawError bool
	// silentError reports a failure with no content, which the SDK's typed
	// helper never produces but an external server may send.
	silentError bool
}

// server builds an MCP server exposing the tool, wired the way a built-in
// group is — including the panic containment that keeps one misbehaving tool
// from taking the process down. The raw handler is used so the stub controls
// exactly what goes on the wire, including a protocol-level error, which the
// typed helper would convert into a tool result.
func (s *stubTool) server() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: s.name, Version: "v0"}, nil)
	srv.AddTool(&mcp.Tool{
		Name:        s.name,
		Description: "stub",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := map[string]any{}
		if raw := req.Params.Arguments; len(raw) > 0 {
			_ = json.Unmarshal(raw, &args)
		}
		s.mu.Lock()
		s.calls = append(s.calls, args)
		panics, text, toolErr, rawError, silent := s.panics, s.text, s.toolErr, s.rawError, s.silentError
		s.mu.Unlock()

		if panics {
			panic("boom")
		}
		if rawError {
			return nil, toolErr
		}
		if silent {
			return &mcp.CallToolResult{IsError: true}, nil
		}
		if toolErr != nil {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: toolErr.Error()}},
			}, nil
		}
		result := &mcp.CallToolResult{}
		if text != "" {
			result.Content = []mcp.Content{&mcp.TextContent{Text: text}}
		}
		return result, nil
	})
	// Added last, as mcpserve does, so the guard is outermost.
	srv.AddReceivingMiddleware(mcpserve.RecoverMiddleware(s.name, nil))
	return srv
}

func (s *stubTool) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *stubTool) recorded() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.calls...)
}

// newHost builds a tool host offering the reply tool bound to conn plus each
// stub tool's server, and closes it when the test ends. The engine closes the
// host too; mcphost.Close is idempotent.
func newHost(t *testing.T, conn chat.Conn, stubs ...*stubTool) *mcphost.Host {
	t.Helper()
	cfg := mcphost.Config{}
	if conn != nil {
		replyTool := reply.Open(context.Background(), conn)
		cfg.HostTools = []mcphost.HostTool{{Def: reply.Definition(), Handler: replyTool.Handler}}
	}
	for _, stub := range stubs {
		cfg.Servers = append(cfg.Servers, mcphost.InProcess(stub.name, "", stub.server()))
	}
	host, err := mcphost.New(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, host.Close()) })
	return host
}
