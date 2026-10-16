package host

import (
	"context"
	"errors"
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func dialErr(errno syscall.Errno) error {
	return &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", errno)}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func Test_netFailure(t *testing.T) {
	tests := map[string]struct {
		err  error
		want Check
	}{
		"refused":         {err: dialErr(syscall.ECONNREFUSED), want: Check{Name: "ssh", Outcome: OutcomeFail, Detail: "connection refused", Responded: true}},
		"reset":           {err: dialErr(syscall.ECONNRESET), want: Check{Name: "ssh", Outcome: OutcomeFail, Detail: "connection reset", Responded: true}},
		"host-unreach":    {err: dialErr(syscall.EHOSTUNREACH), want: Check{Name: "ssh", Outcome: OutcomeFail, Detail: "no route to host"}},
		"host-down":       {err: dialErr(syscall.EHOSTDOWN), want: Check{Name: "ssh", Outcome: OutcomeFail, Detail: "no answer on the local network (ARP)"}},
		"net-unreach":     {err: dialErr(syscall.ENETUNREACH), want: Check{Name: "ssh", Outcome: OutcomeError, Detail: "network is unreachable from chatops-mcp"}},
		"ctx-deadline":    {err: context.DeadlineExceeded, want: Check{Name: "ssh", Outcome: OutcomeFail, Detail: "no answer within 3s"}},
		"read-deadline":   {err: os.ErrDeadlineExceeded, want: Check{Name: "ssh", Outcome: OutcomeFail, Detail: "no answer within 3s"}},
		"net-timeout":     {err: &net.OpError{Op: "dial", Err: timeoutErr{}}, want: Check{Name: "ssh", Outcome: OutcomeFail, Detail: "no answer within 3s"}},
		"cancelled":       {err: context.Canceled, want: Check{Name: "ssh", Outcome: OutcomeError, Detail: "check cancelled"}},
		"other-local-err": {err: errors.New("too many open files"), want: Check{Name: "ssh", Outcome: OutcomeError, Detail: "too many open files"}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, netFailure("ssh", tc.err, 3*time.Second))
		})
	}
}
