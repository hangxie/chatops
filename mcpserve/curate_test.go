package mcpserve_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

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
		want      string
		logged    bool
		unchanged bool
	}{
		"nil":               {err: nil, unchanged: true},
		"user error":        {err: mcpserve.UserError("k8s: requires a kind"), want: "k8s: requires a kind"},
		"wrapped user":      {err: fmt.Errorf("get: %w", mcpserve.UserError("k8s: requires a kind")), want: "get: k8s: requires a kind"},
		"cancelled":         {err: context.Canceled, want: context.Canceled.Error()},
		"deadline":          {err: context.DeadlineExceeded, want: context.DeadlineExceeded.Error()},
		"wrapped cancelled": {err: fmt.Errorf("call: %w", context.Canceled), want: "call: context canceled"},
		"operational":       {err: leaky, want: notice, logged: true},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			got := mcpserve.Curate(slog.New(slog.NewTextHandler(&logs, nil)), "k8s", "k8s-get", notice, tc.err)

			if tc.unchanged {
				require.NoError(t, got)
			} else {
				require.EqualError(t, got, tc.want)
			}

			if tc.logged {
				// The detail is kept, just not where the requester can see it.
				require.Contains(t, logs.String(), "tool call failed")
				require.Contains(t, logs.String(), "10.1.2.3")
				require.Contains(t, logs.String(), "group=k8s")
				require.Contains(t, logs.String(), "tool=k8s-get")
				require.NotContains(t, got.Error(), "10.1.2.3")
				require.NotContains(t, got.Error(), "serviceaccount")
				return
			}
			require.Empty(t, logs.String(), "nothing to hide should not be logged as a failure")
		})
	}
}

func Test_Curate_nil_logger(t *testing.T) {
	// A nil logger discards the detail; the replacement still happens, which
	// is the part that matters for what reaches chat.
	got := mcpserve.Curate(nil, "k8s", "k8s-get", "notice", errors.New("10.1.2.3 refused"))

	require.EqualError(t, got, "notice")
}
