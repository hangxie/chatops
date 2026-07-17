package ping_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/tool"
	"github.com/hangxie/chatops/tool/ping"
)

func Test_Opener_via_registry(t *testing.T) {
	reg := tool.NewRegistry(tool.Backend{Scheme: ping.Scheme, Opener: ping.Opener})

	testCases := map[string]struct {
		url    string
		errMsg string
	}{
		"bare-url":    {url: "ping://"},
		"scheme-only": {url: "ping:"},
		// url.Parse drops a bare trailing "#", making it
		// indistinguishable from the bare URL, so it is accepted.
		"empty-fragment": {url: "ping://#"},
		"host":           {url: "ping://somehost", errMsg: "takes no endpoint"},
		"host-port":      {url: "ping://somehost:1234", errMsg: "takes no endpoint"},
		"path":           {url: "ping:///some/path", errMsg: "takes no endpoint"},
		"query":          {url: "ping://?cred-prefix=x", errMsg: "takes no endpoint"},
		"empty-query":    {url: "ping://?", errMsg: "takes no endpoint"},
		"userinfo":       {url: "ping://secret@", errMsg: "takes no endpoint"},
		"fragment":       {url: "ping://#fragment", errMsg: "takes no endpoint"},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			tl, err := reg.Open(context.Background(), tc.url, nil)
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			defer func() {
				require.NoError(t, tl.Close())
			}()
			result, err := tl.Invoke(context.Background(), tool.Call{Action: "ping"})
			require.NoError(t, err)
			require.Equal(t, "pong", result.Text)
		})
	}
}

func Test_Invoke(t *testing.T) {
	tl, err := ping.Open(context.Background())
	require.NoError(t, err)
	defer func() {
		require.NoError(t, tl.Close())
	}()

	testCases := map[string]struct {
		call  tool.Call
		errIs error
	}{
		"ping":                  {call: tool.Call{Action: "ping"}},
		"ping-ignores-target":   {call: tool.Call{Action: "ping", Target: "somewhere"}},
		"ping-ignores-params":   {call: tool.Call{Action: "ping", Parameters: map[string]string{"count": "3"}}},
		"unknown-action":        {call: tool.Call{Action: "pong"}, errIs: tool.ErrUnknownAction},
		"empty-action":          {call: tool.Call{}, errIs: tool.ErrUnknownAction},
		"case-sensitive-action": {call: tool.Call{Action: "PING"}, errIs: tool.ErrUnknownAction},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			result, err := tl.Invoke(context.Background(), tc.call)
			if tc.errIs != nil {
				require.ErrorIs(t, err, tc.errIs)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tool.Result{Text: "pong"}, result)
		})
	}
}

func Test_Invoke_cancelled_context(t *testing.T) {
	tl, err := ping.Open(context.Background())
	require.NoError(t, err)
	defer func() {
		require.NoError(t, tl.Close())
	}()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = tl.Invoke(ctx, tool.Call{Action: "ping"})
	require.ErrorIs(t, err, context.Canceled)
}

func Test_Close_is_idempotent(t *testing.T) {
	tl, err := ping.Open(context.Background())
	require.NoError(t, err)
	require.NoError(t, tl.Close())
	require.NoError(t, tl.Close())
}
