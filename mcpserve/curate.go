package mcpserve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// A tool result is relayed to the requester, so an error returned by a tool
// handler is output, not a log line. Two kinds get confused easily:
//
//   - What the requester did: an argument that is missing, malformed, or
//     names something that does not exist. Telling them is the whole point.
//   - What the system behind the tool did: an endpoint that refused, timed
//     out, or could not be resolved. Those carry addresses, file paths, and
//     the identity an authorization check rejected.
//
// Nothing can tell the two apart after the fact — by the time a result exists
// the error has already been flattened to text — so a tool marks the first
// kind as it raises it, with UserError, and Curate replaces everything else.

// userError marks an error whose message is safe to show the requester.
type userError struct{ err error }

func (e userError) Error() string { return e.err.Error() }

func (e userError) Unwrap() error { return e.err }

// UserError marks an error as safe to relay to the requester. Use it for
// anything the requester can act on: a missing argument, an unknown name, a
// value outside the allowed set.
func UserError(format string, args ...any) error {
	return userError{err: fmt.Errorf(format, args...)}
}

// IsUserError reports whether err was marked with UserError.
func IsUserError(err error) bool {
	var marked userError
	return errors.As(err, &marked)
}

// Curate returns the error a tool handler should report.
//
// A UserError passes through unchanged, as does a cancellation, whose message
// carries nothing. Anything else is logged with its detail and replaced by
// notice, so the requester learns that the call failed without learning where
// it failed. A nil logger discards the detail.
func Curate(logger *slog.Logger, group, tool, notice string, err error) error {
	if err == nil || IsUserError(err) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// Shutting down with a call in flight is ordinary, not a fault, and
		// the message says nothing worth hiding.
		return err
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	logger.Error("tool call failed", "group", group, "tool", tool, "error", err.Error())
	return errors.New(notice)
}
