package openaichatcompletions

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/planner"
	"github.com/hangxie/chatops/tool"
	"github.com/hangxie/chatops/tool/reply"
)

func Test_stepsFromMessage(t *testing.T) {
	const conv = "C123"
	toolCallStatus := toolCall{
		Type:     "function",
		Function: functionCall{Name: "status-check", Arguments: `{"target":"github","parameters":{"verbose":"true"}}`},
	}
	replyCall := toolCall{
		Type:     "function",
		Function: functionCall{Name: "reply", Arguments: `{"text":"on it"}`},
	}

	testCases := map[string]struct {
		msg     respMessage
		want    []planner.Step
		wantErr string
	}{
		"content-only": {
			msg:  respMessage{Content: "  hello there  "},
			want: []planner.Step{wantReply(conv, "hello there")},
		},
		"reply-function": {
			msg:  respMessage{ToolCalls: []toolCall{replyCall}},
			want: []planner.Step{wantReply(conv, "on it")},
		},
		"single-tool-call": {
			msg: respMessage{ToolCalls: []toolCall{toolCallStatus}},
			want: []planner.Step{{
				Tool: "status://",
				Call: tool.Call{Action: "check", Target: "github", Parameters: map[string]string{"verbose": "true"}},
			}},
		},
		"content-and-tool-call": {
			msg: respMessage{Content: "checking", ToolCalls: []toolCall{toolCallStatus}},
			want: []planner.Step{
				wantReply(conv, "checking"),
				{Tool: "status://", Call: tool.Call{Action: "check", Target: "github", Parameters: map[string]string{"verbose": "true"}}},
			},
		},
		"target-trimmed": {
			// A target with surrounding whitespace is normalized before it
			// reaches the tool, matching validateArgs' trimmed check.
			msg: respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-check", Arguments: `{"target":"  github  "}`}}}},
			want: []planner.Step{{
				Tool: "status://",
				Call: tool.Call{Action: "check", Target: "github"},
			}},
		},
		"other-scheme": {
			msg:  respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "ping-ping", Arguments: `{}`}}}},
			want: []planner.Step{{Tool: "ping://", Call: tool.Call{Action: "ping"}}},
		},
		"function-no-arguments": {
			// The action comes from the function name, so a call to a
			// no-argument action with no arguments is a valid step.
			msg:  respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "ping-ping"}}}},
			want: []planner.Step{{Tool: "ping://", Call: tool.Call{Action: "ping"}}},
		},
		"typed-parameters": {
			// A typed tool function may send non-string parameter values;
			// they are validated against the schema then stringified.
			msg: respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-check", Arguments: `{"target":"x","parameters":{"replicas":3,"force":true,"note":"hi"}}`}}}},
			want: []planner.Step{{
				Tool: "status://",
				Call: tool.Call{Action: "check", Target: "x", Parameters: map[string]string{"replicas": "3", "force": "true", "note": "hi"}},
			}},
		},
		"null-declared-parameter-dropped": {
			// A declared optional parameter sent as null counts as absent.
			msg: respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-check", Arguments: `{"target":"x","parameters":{"note":null}}`}}}},
			want: []planner.Step{{
				Tool: "status://",
				Call: tool.Call{Action: "check", Target: "x"},
			}},
		},
		"empty-message": {
			msg:  respMessage{},
			want: nil,
		},
		"blank-content-only": {
			msg:  respMessage{Content: "   \n\t "},
			want: nil,
		},
		"malformed-arguments": {
			msg:     respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-check", Arguments: "{not json"}}}},
			wantErr: "decode arguments",
		},
		"unavailable-function": {
			msg:     respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "deploy-go", Arguments: `{}`}}}},
			wantErr: `unavailable function "deploy-go"`,
		},
		"empty-function-name": {
			msg:     respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "", Arguments: `{}`}}}},
			wantErr: "unavailable function",
		},
		"empty-reply-text": {
			msg:     respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "reply", Arguments: `{"text":"  "}`}}}},
			wantErr: "reply call has empty text",
		},
		// Argument validation against the descriptor.
		"missing-required-target": {
			msg:     respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-check", Arguments: `{}`}}}},
			wantErr: `function "status-check" requires a target`,
		},
		"target-not-allowed": {
			msg:     respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-list", Arguments: `{"target":"x"}`}}}},
			wantErr: `function "status-list" does not take a target`,
		},
		"undeclared-parameter": {
			msg:     respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-check", Arguments: `{"target":"x","parameters":{"bogus":"y"}}`}}}},
			wantErr: `undeclared parameter "bogus"`,
		},
		"missing-required-parameter": {
			msg:     respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-scale", Arguments: `{"target":"x"}`}}}},
			wantErr: `missing required parameter "replicas"`,
		},
		"wrong-boolean-type": {
			msg:     respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-check", Arguments: `{"target":"x","parameters":{"force":"yes"}}`}}}},
			wantErr: `parameter "force": must be a boolean`,
		},
		"wrong-string-type": {
			msg:     respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-check", Arguments: `{"target":"x","parameters":{"note":5}}`}}}},
			wantErr: `parameter "note": must be a string`,
		},
		"wrong-number-type": {
			msg:     respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-check", Arguments: `{"target":"x","parameters":{"ratio":"nope"}}`}}}},
			wantErr: `parameter "ratio": must be a number`,
		},
		"number-parameter-valid": {
			msg: respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-check", Arguments: `{"target":"x","parameters":{"ratio":1.5}}`}}}},
			want: []planner.Step{{
				Tool: "status://",
				Call: tool.Call{Action: "check", Target: "x", Parameters: map[string]string{"ratio": "1.5"}},
			}},
		},
		"untyped-parameter-validated-as-string": {
			msg: respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-check", Arguments: `{"target":"x","parameters":{"tag":"v1"}}`}}}},
			want: []planner.Step{{
				Tool: "status://",
				Call: tool.Call{Action: "check", Target: "x", Parameters: map[string]string{"tag": "v1"}},
			}},
		},
		"non-integer-value": {
			msg:     respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-scale", Arguments: `{"target":"x","parameters":{"replicas":2.5}}`}}}},
			wantErr: `parameter "replicas": must be an integer`,
		},
		"non-numeric-integer": {
			msg:     respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-scale", Arguments: `{"target":"x","parameters":{"replicas":"x"}}`}}}},
			wantErr: `parameter "replicas": must be an integer`,
		},
	}

	funcs := map[string]toolFunc{
		"status-check": {scheme: "status", action: tool.Action{
			Name: "check", TakesTarget: true,
			Parameters: []tool.Param{
				{Name: "verbose", Type: "string"},
				{Name: "replicas", Type: "integer"},
				{Name: "force", Type: "boolean"},
				{Name: "note", Type: "string"},
				{Name: "ratio", Type: "number"},
				{Name: "tag"}, // no type -> validated as string
			},
		}},
		"status-list": {scheme: "status", action: tool.Action{Name: "list"}},
		"status-scale": {scheme: "status", action: tool.Action{
			Name: "scale", TakesTarget: true,
			Parameters: []tool.Param{{Name: "replicas", Type: "integer", Required: true}},
		}},
		"ping-ping": {scheme: "ping", action: tool.Action{Name: "ping"}},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			plan, err := stepsFromMessage(tc.msg, conv, funcs)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, plan.Steps)
		})
	}
}

// wantReply is the reply step the mapping is expected to emit.
func wantReply(conv, text string) planner.Step {
	return planner.Step{Tool: reply.URL, Call: tool.Call{
		Action:     "send",
		Target:     conv,
		Parameters: map[string]string{"text": text},
	}}
}
