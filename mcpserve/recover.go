package mcpserve

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RecoverMiddleware turns a panic in a tool handler into a tool error.
//
// The SDK runs each request on its own goroutine and does not recover panics,
// so an unrecovered one takes down the whole process — which for an
// in-process group means the bot itself. A misbehaving tool must cost its own
// call and nothing more, so the panic is contained here, at the server, where
// it protects the group whether it runs in process or out of it.
//
// The recovered value and its stack go to logger (nil discards them); the
// caller is told only that the tool failed, so internal detail does not reach
// chat.
// It is applied to every built-in group by Registry.Server; it is exported so
// a server assembled directly — in a test, or by a caller wiring its own —
// gets the same containment.
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
				logger.Error("tool panicked",
					"group", group, "method", method,
					"panic", fmt.Sprint(recovered), "stack", string(debug.Stack()))
				result = nil
				err = fmt.Errorf("%s: %s panicked", group, method)
			}()
			return next(ctx, method, req)
		}
	}
}
