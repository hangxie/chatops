package mcpserve_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/mcpserve"
)

func Test_UserError(t *testing.T) {
	sentinel := errors.New("underlying")
	marked := mcpserve.UserError("k8s: no pod named %q: %w", "web-0", sentinel)

	require.Equal(t, `k8s: no pod named "web-0": underlying`, marked.Error())
	require.True(t, mcpserve.IsUserError(marked))
	// Marking must not hide what it wraps.
	require.ErrorIs(t, marked, sentinel)
}

func Test_IsUserError(t *testing.T) {
	testCases := map[string]struct {
		err  error
		want bool
	}{
		"nil":      {err: nil, want: false},
		"plain":    {err: errors.New("boom"), want: false},
		"marked":   {err: mcpserve.UserError("bad argument"), want: true},
		"wrapped":  {err: fmt.Errorf("outer: %w", mcpserve.UserError("bad argument")), want: true},
		"inverted": {err: mcpserve.UserError("outer: %w", errors.New("inner")), want: true},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, mcpserve.IsUserError(tc.err))
		})
	}
}

func Test_Curate(t *testing.T) {
	const notice = "k8s: the cluster could not be reached"
	leaky := errors.New(`Get "https://10.1.2.3:6443/api": forbidden for user "system:serviceaccount:ops:bot"`)

	testCases := map[string]struct {
		err       error
		cancelled bool
		want      string
		logged    bool
		unchanged bool
	}{
		"nil":          {err: nil, unchanged: true},
		"user error":   {err: mcpserve.UserError("k8s: requires a kind"), want: "k8s: requires a kind"},
		"wrapped user": {err: fmt.Errorf("get: %w", mcpserve.UserError("k8s: requires a kind")), want: "get: k8s: requires a kind"},
		"operational":  {err: leaky, want: notice, logged: true},
		"caller went away": {
			err: context.Canceled, cancelled: true, want: "k8s: call cancelled",
		},
		// A cancellation error with a live context came from inside the call,
		// not from the caller, so it is treated like any other failure.
		"cancelled inside the call": {err: context.Canceled, want: notice, logged: true},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			ctx := context.Background()
			if tc.cancelled {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}

			got := mcpserve.Curate(ctx, slog.New(slog.NewTextHandler(&logs, nil)), "k8s", "k8s-get", notice, tc.err)

			if tc.unchanged {
				require.NoError(t, got)
			} else {
				require.EqualError(t, got, tc.want)
			}

			if tc.logged {
				// The detail is kept, just not where the requester can see it.
				require.Contains(t, logs.String(), "tool call failed")
				require.Contains(t, logs.String(), "group=k8s")
				require.Contains(t, logs.String(), "tool=k8s-get")
				return
			}
			require.Empty(t, logs.String(), "nothing to hide should not be logged as a failure")
		})
	}
}

// Test_Curate_hides_a_client_timeout is the reason the caller's context
// decides, rather than the error. An http.Client with a Timeout reports its
// own expiry as an error matching context.DeadlineExceeded, wrapped in a
// *url.Error carrying the address it was calling — so trusting the error
// would relay exactly what Curate exists to hide, for the most ordinary HTTP
// client setup there is.
func Test_Curate_hides_a_client_timeout(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(time.Second)
	}))
	defer slow.Close()

	client := &http.Client{Timeout: 20 * time.Millisecond}
	_, err := client.Get(slow.URL + "/api/v1/namespaces/ops/secrets")
	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded, "the premise: it looks like a cancellation")
	require.Contains(t, err.Error(), slow.URL, "the premise: it carries the address")

	var logs bytes.Buffer
	got := mcpserve.Curate(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)),
		"status", "status-check", "status: the service could not be checked", err)

	require.EqualError(t, got, "status: the service could not be checked")
	require.NotContains(t, got.Error(), slow.URL)
	require.Contains(t, logs.String(), slow.URL)
}

func Test_Curate_nil_logger(t *testing.T) {
	// A nil logger discards the detail; the replacement still happens, which
	// is the part that matters for what reaches chat.
	got := mcpserve.Curate(context.Background(), nil, "k8s", "k8s-get", "notice", errors.New("10.1.2.3 refused"))

	require.EqualError(t, got, "notice")
}
