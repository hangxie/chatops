package mcpserve

import (
	"context"
	"errors"
	"io"
	"net"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

// codeServerClosing is the JSON-RPC error code the SDK reports when a
// connection ends. It is matched by code rather than by sentinel because the
// SDK keeps its sentinel unexported and formats the underlying cause with %v,
// so neither errors.Is(err, io.EOF) nor any exported error matches it.
const codeServerClosing = -32004

// GracefulStop reports whether a server's Run ended for an ordinary reason
// rather than a failure.
//
// A server stops when its peer goes away — a host closing a stdio pipe, or the
// process closing an in-process transport during shutdown — and when its
// context is cancelled. None of those are worth reporting as errors, and
// treating them as such makes every clean shutdown look like a fault.
func GracefulStop(err error) bool {
	switch {
	case err == nil,
		errors.Is(err, context.Canceled),
		errors.Is(err, io.EOF),
		errors.Is(err, io.ErrClosedPipe),
		errors.Is(err, net.ErrClosed):
		return true
	}
	var wire *jsonrpc.Error
	return errors.As(err, &wire) && wire.Code == codeServerClosing
}
