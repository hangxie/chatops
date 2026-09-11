package openaichatcompletions

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/planner"
	"github.com/hangxie/chatops/tool/reply"
)

// offered is the function set the tests treat as available to the model.
var offered = map[string]bool{
	"reply":        true,
	"status-check": true,
	"status-list":  true,
}

func Test_stepsFromMessage(t *testing.T) {
	testCases := map[string]struct {
		msg    respMessage
		want   []planner.Step
		errMsg string
	}{
		"prose-becomes-reply": {
			msg:  respMessage{Content: "all good"},
			want: []planner.Step{replyStep("all good")},
		},
		"blank-prose-ignored": {
			msg:  respMessage{Content: "   "},
			want: nil,
		},
		"reply-call": {
			msg: respMessage{ToolCalls: []toolCall{
				{Function: functionCall{Name: "reply", Arguments: `{"text":"on it"}`}},
			}},
			want: []planner.Step{{Tool: "reply", Arguments: map[string]any{"text": "on it"}}},
		},
		"tool-call": {
			msg: respMessage{ToolCalls: []toolCall{
				{Function: functionCall{Name: "status-check", Arguments: `{"service":"github"}`}},
			}},
			want: []planner.Step{{Tool: "status-check", Arguments: map[string]any{"service": "github"}}},
		},
		"typed-arguments-survive": {
			msg: respMessage{ToolCalls: []toolCall{
				{Function: functionCall{Name: "status-check", Arguments: `{"service":"x","verbose":true,"replicas":3}`}},
			}},
			// Arguments keep their JSON types: the tool's schema, enforced by
			// the server, decides what they mean.
			want: []planner.Step{{Tool: "status-check", Arguments: map[string]any{
				"service": "x", "verbose": true, "replicas": float64(3),
			}}},
		},
		"no-arguments": {
			msg: respMessage{ToolCalls: []toolCall{
				{Function: functionCall{Name: "status-list"}},
			}},
			want: []planner.Step{{Tool: "status-list"}},
		},
		"empty-arguments-object": {
			msg: respMessage{ToolCalls: []toolCall{
				{Function: functionCall{Name: "status-list", Arguments: `{}`}},
			}},
			want: []planner.Step{{Tool: "status-list", Arguments: map[string]any{}}},
		},
		"blank-arguments-string": {
			msg: respMessage{ToolCalls: []toolCall{
				{Function: functionCall{Name: "status-list", Arguments: "  "}},
			}},
			want: []planner.Step{{Tool: "status-list"}},
		},
		"prose-and-calls-in-order": {
			msg: respMessage{
				Content: "checking",
				ToolCalls: []toolCall{
					{Function: functionCall{Name: "status-check", Arguments: `{"service":"github"}`}},
				},
			},
			want: []planner.Step{
				replyStep("checking"),
				{Tool: "status-check", Arguments: map[string]any{"service": "github"}},
			},
		},
		"several-calls": {
			msg: respMessage{ToolCalls: []toolCall{
				{Function: functionCall{Name: "status-list"}},
				{Function: functionCall{Name: "status-check", Arguments: `{"service":"slack"}`}},
			}},
			want: []planner.Step{
				{Tool: "status-list"},
				{Tool: "status-check", Arguments: map[string]any{"service": "slack"}},
			},
		},
		"empty-message": {
			msg:  respMessage{},
			want: nil,
		},
		"unavailable-function": {
			msg: respMessage{ToolCalls: []toolCall{
				{Function: functionCall{Name: "rm-rf", Arguments: `{}`}},
			}},
			errMsg: `called unavailable function "rm-rf"`,
		},
		"bad-arguments-json": {
			msg: respMessage{ToolCalls: []toolCall{
				{Function: functionCall{Name: "status-check", Arguments: `{"service":`}},
			}},
			errMsg: `decode arguments for "status-check"`,
		},
		"arguments-not-an-object": {
			msg: respMessage{ToolCalls: []toolCall{
				{Function: functionCall{Name: "status-check", Arguments: `"github"`}},
			}},
			errMsg: `decode arguments for "status-check"`,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			plan, err := stepsFromMessage(tc.msg, offered)
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, plan.Steps)
		})
	}
}

func Test_replyStep(t *testing.T) {
	step := replyStep("hello")
	require.Equal(t, reply.ToolName, step.Tool)
	require.Equal(t, map[string]any{"text": "hello"}, step.Arguments)
}

func Test_decodeArguments(t *testing.T) {
	testCases := map[string]struct {
		raw   string
		want  map[string]any
		isErr bool
	}{
		"empty":   {raw: "", want: nil},
		"blank":   {raw: "   ", want: nil},
		"object":  {raw: `{"a":1}`, want: map[string]any{"a": float64(1)}},
		"null":    {raw: "null", want: nil},
		"invalid": {raw: "{", isErr: true},
		"array":   {raw: "[1]", isErr: true},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got, err := decodeArguments(tc.raw)
			if tc.isErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
