package mcpserve

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RecoverMiddleware contains a panic in a tool handler.
//
// The SDK runs each request on its own goroutine and does not recover panics,
// so an unrecovered one takes down the whole process — which for an
// in-process group means the bot itself. A misbehaving tool must cost its own
// call and nothing more, so the panic is contained here, at the server, where
// it protects the group whether it runs in process or out of it.
//
// The call fails as a protocol error rather than as a tool result. That is
// deliberate: a tool result is relayed to the requester, and a panic has
// nothing to tell them. The recovered value and its stack go to logger (nil
// discards them), and the requester gets the caller's generic failure notice.
//
// It is applied to every built-in group by Registry.Server, added after the
// group so that it is outermost: the SDK wraps each middleware around what is
// already registered, so a guard added first would sit inside anything the
// group installed. A server assembled directly should add it last for the
// same reason.
func RecoverMiddleware(group string, logger *slog.Logger) mcp.Middleware {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (result mcp.Result, err error) {
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}
				name := calledTool(req)
				logger.Error("tool panicked",
					"group", group, "method", method, "tool", name,
					"panic", fmt.Sprint(recovered), "stack", string(debug.Stack()))
				result = nil
				err = fmt.Errorf("%s: tool %q panicked", group, name)
			}()
			return next(ctx, method, req)
		}
	}
}

// calledTool names the tool a request invokes. Every call the middleware
// guards is a tools/call, so the method alone would say nothing useful about
// which tool misbehaved.
func calledTool(req mcp.Request) string {
	call, ok := req.(*mcp.CallToolRequest)
	if !ok || call.Params == nil {
		return "unknown"
	}
	return call.Params.Name
}
