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
// A UserError passes through unchanged. A call that ended because the caller
// went away is reported as cancelled — ordinary during shutdown, and not
// worth logging as a fault on every restart. Everything else is logged with
// its detail and replaced by notice, so the requester learns that the call
// failed without learning where it failed. A nil logger discards the detail.
//
// Whether the caller went away is decided by ctx, not by the error. An
// http.Client with a Timeout reports its own expiry as an error that matches
// context.DeadlineExceeded, wrapped in a *url.Error carrying the address it
// was calling — so trusting the error would relay exactly what this exists to
// hide, for the most ordinary HTTP client setup there is. A deadline that
// fired inside the call is an operational failure like any other.
func Curate(ctx context.Context, logger *slog.Logger, group, tool, notice string, err error) error {
	if err == nil || IsUserError(err) {
		return err
	}
	if ctx.Err() != nil {
		return fmt.Errorf("%s: call cancelled", group)
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	logger.Error("tool call failed", "group", group, "tool", tool, "error", err.Error())
	return errors.New(notice)
}
