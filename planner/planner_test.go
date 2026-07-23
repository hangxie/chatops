package planner_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/planner"
	"github.com/hangxie/chatops/tool"
)

// fakePlanner is a minimal planner.Planner used to exercise the
// interface contract the way a real planner is expected to behave: it
// plans a ping step for the message "go", a clarifying reply for any
// other message, and fails on empty messages.
type fakePlanner struct{}

func (f *fakePlanner) Plan(_ context.Context, req planner.Request) (planner.Plan, error) {
	if strings.TrimSpace(req.Text) == "" {
		return planner.Plan{}, fmt.Errorf("fake: empty message")
	}
	if req.Text == "go" {
		return planner.Plan{Steps: []planner.Step{
			{Tool: "ping://", Call: tool.Call{}},
		}}, nil
	}
	return planner.Plan{Steps: []planner.Step{
		{Tool: "reply://", Call: tool.Call{
			Arguments: map[string]string{"text": "what do you mean, " + req.Sender + "?"},
		}},
	}}, nil
}

func (f *fakePlanner) Close() error {
	return nil
}

func Test_Planner_contract(t *testing.T) {
	var p planner.Planner = &fakePlanner{}
	defer func() {
		require.NoError(t, p.Close())
	}()

	testCases := map[string]struct {
		req      planner.Request
		expected planner.Plan
		errMsg   string
	}{
		"tool-step": {
			req: planner.Request{Text: "go", ConversationID: "conv-1", Sender: "alice"},
			expected: planner.Plan{Steps: []planner.Step{
				{Tool: "ping://", Call: tool.Call{}},
			}},
		},
		"reply-step": {
			req: planner.Request{Text: "hmm", ConversationID: "conv-1", Sender: "alice"},
			expected: planner.Plan{Steps: []planner.Step{
				{Tool: "reply://", Call: tool.Call{
					Arguments: map[string]string{"text": "what do you mean, alice?"},
				}},
			}},
		},
		"error": {req: planner.Request{Text: "  "}, errMsg: "empty message"},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			plan, err := p.Plan(context.Background(), tc.req)
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expected, plan)
		})
	}
}
