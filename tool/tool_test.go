package tool_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/tool"
)

// fakeTool is a minimal tool.Tool used to exercise the interface
// contract the way a real tool is expected to behave: it supports a
// single "echo" action and reports ErrUnknownAction for anything else.
type fakeTool struct{}

func (f *fakeTool) Invoke(_ context.Context, call tool.Call) (tool.Result, error) {
	if call.Action != "echo" {
		return tool.Result{}, fmt.Errorf("%q: %w", call.Action, tool.ErrUnknownAction)
	}
	return tool.Result{Text: "echo " + call.Target, Details: call.Parameters}, nil
}

func (f *fakeTool) Close() error {
	return nil
}

func Test_Tool_contract(t *testing.T) {
	var tl tool.Tool = &fakeTool{}
	defer func() {
		require.NoError(t, tl.Close())
	}()

	testCases := map[string]struct {
		call     tool.Call
		expected tool.Result
		errIs    error
	}{
		"known-action": {
			call:     tool.Call{Action: "echo", Target: "world"},
			expected: tool.Result{Text: "echo world"},
		},
		"known-action-with-parameters": {
			call:     tool.Call{Action: "echo", Target: "world", Parameters: map[string]string{"lang": "en"}},
			expected: tool.Result{Text: "echo world", Details: map[string]string{"lang": "en"}},
		},
		"unknown-action": {call: tool.Call{Action: "no-such-action"}, errIs: tool.ErrUnknownAction},
		"empty-action":   {call: tool.Call{}, errIs: tool.ErrUnknownAction},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			result, err := tl.Invoke(context.Background(), tc.call)
			if tc.errIs != nil {
				require.ErrorIs(t, err, tc.errIs)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expected, result)
		})
	}
}

func Test_ErrUnknownAction_is_stable_sentinel(t *testing.T) {
	require.EqualError(t, tool.ErrUnknownAction, "unknown action")
	require.True(t, errors.Is(fmt.Errorf("wrapped: %w", tool.ErrUnknownAction), tool.ErrUnknownAction))
}
