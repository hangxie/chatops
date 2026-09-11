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
				what := panicked(method, req)
				logger.Error("handler panicked",
					"group", group, "method", method, "handler", what,
					"panic", fmt.Sprint(recovered), "stack", string(debug.Stack()))
				result = nil
				err = fmt.Errorf("%s: %s panicked", group, what)
			}()
			return next(ctx, method, req)
		}
	}
}

// panicked names what misbehaved.
//
// Receiving middleware wraps every method, not only tools/call — initialize,
// tools/list and notifications go through it too — so the method is the
// answer unless the request names a tool, in which case the method alone
// would not say which one.
func panicked(method string, req mcp.Request) string {
	call, ok := req.(*mcp.CallToolRequest)
	if !ok || call.Params == nil || call.Params.Name == "" {
		return method
	}
	return fmt.Sprintf("tool %q", call.Params.Name)
}
