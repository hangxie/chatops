package tool_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/tool"
)

// fakeTool is a minimal tool.Tool used to exercise the interface
// contract the way a real tool is expected to behave: a single-intent
// tool that echoes its "subject" argument and errors on an empty one.
type fakeTool struct{}

func (f *fakeTool) Invoke(_ context.Context, call tool.Call) (tool.Result, error) {
	subject := call.Arguments["subject"]
	if subject == "" {
		return tool.Result{}, errors.New("echo: missing subject")
	}
	return tool.Result{Text: "echo " + subject, Details: call.Arguments}, nil
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
		wantErr  bool
	}{
		"with-subject": {
			call:     tool.Call{Arguments: map[string]string{"subject": "world"}},
			expected: tool.Result{Text: "echo world", Details: map[string]string{"subject": "world"}},
		},
		"with-extra-arguments": {
			call:     tool.Call{Arguments: map[string]string{"subject": "world", "lang": "en"}},
			expected: tool.Result{Text: "echo world", Details: map[string]string{"subject": "world", "lang": "en"}},
		},
		"missing-subject": {call: tool.Call{Arguments: map[string]string{"lang": "en"}}, wantErr: true},
		"empty-call":      {call: tool.Call{}, wantErr: true},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			result, err := tl.Invoke(context.Background(), tc.call)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expected, result)
		})
	}
}
