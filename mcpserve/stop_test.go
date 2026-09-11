package mcpserve_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/mcpserve"
)

func Test_GracefulStop(t *testing.T) {
	testCases := map[string]struct {
		err  error
		want bool
	}{
		"nil":            {err: nil, want: true},
		"cancelled":      {err: context.Canceled, want: true},
		"wrapped-cancel": {err: fmt.Errorf("serve: %w", context.Canceled), want: true},
		"eof":            {err: io.EOF, want: true},
		"wrapped-eof":    {err: fmt.Errorf("read: %w", io.EOF), want: true},
		"closed-pipe":    {err: io.ErrClosedPipe, want: true},
		"net-closed":     {err: net.ErrClosed, want: true},
		"wrapped-net":    {err: fmt.Errorf("conn: %w", net.ErrClosed), want: true},
		"deadline":       {err: context.DeadlineExceeded, want: false},
		"other":          {err: errors.New("disk on fire"), want: false},
		"wrapped-other":  {err: fmt.Errorf("serve: %w", errors.New("boom")), want: false},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, mcpserve.GracefulStop(tc.err))
		})
	}
}
