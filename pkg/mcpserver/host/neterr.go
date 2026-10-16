package host

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"
)

// netFailure classifies a failed connect or send: only errors that say something about the target are failures.
func netFailure(name string, err error, timeout time.Duration) Check {
	fail := func(detail string, responded bool) Check {
		return Check{Name: name, Outcome: OutcomeFail, Detail: detail, Responded: responded}
	}
	var netErr net.Error
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return fail("connection refused", true)
	case errors.Is(err, syscall.ECONNRESET):
		return fail("connection reset", true)
	case errors.Is(err, syscall.EHOSTUNREACH):
		return fail("no route to host", false)
	case errors.Is(err, syscall.EHOSTDOWN):
		return fail("no answer on the local network (ARP)", false)
	case errors.Is(err, context.Canceled):
		return Check{Name: name, Outcome: OutcomeError, Detail: "check cancelled"}
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return fail(fmt.Sprintf("no answer within %s", timeout), false)
	case errors.Is(err, syscall.ENETUNREACH):
		return Check{Name: name, Outcome: OutcomeError, Detail: "network is unreachable from chatops-mcp"}
	}
	return Check{Name: name, Outcome: OutcomeError, Detail: err.Error()}
}
