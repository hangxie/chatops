package openaichatcompletions

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/planner"
	"github.com/hangxie/chatops/tool"
	"github.com/hangxie/chatops/tool/reply"
)

func Test_stepsFromMessage(t *testing.T) {
	toolCallStatus := toolCall{
		Type:     "function",
		Function: functionCall{Name: "status-check", Arguments: `{"service":"github","verbose":"true"}`},
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
			want: []planner.Step{wantReply("hello there")},
		},
		"reply-function": {
			msg:  respMessage{ToolCalls: []toolCall{replyCall}},
			want: []planner.Step{wantReplyCall("on it", "call_0")},
		},
		"single-tool-call": {
			msg:  respMessage{ToolCalls: []toolCall{toolCallStatus}},
			want: []planner.Step{wantTool("status-check", map[string]string{"service": "github", "verbose": "true"})},
		},
		"content-and-tool-call": {
			msg: respMessage{Content: "checking", ToolCalls: []toolCall{toolCallStatus}},
			want: []planner.Step{
				wantReply("checking"),
				wantTool("status-check", map[string]string{"service": "github", "verbose": "true"}),
			},
		},
		"tool-with-no-arguments-object": {
			msg:  respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-list", Arguments: `{}`}}}},
			want: []planner.Step{wantTool("status-list", nil)},
		},
		"tool-with-empty-arguments": {
			// A tool with no required arguments and no arguments string is a
			// valid step carrying an empty call.
			msg:  respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-list"}}}},
			want: []planner.Step{wantTool("status-list", nil)},
		},
		"typed-parameters": {
			// A typed tool function may send non-string values; they are
			// validated against the schema then stringified.
			msg:  respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-check", Arguments: `{"service":"x","replicas":3,"force":true,"note":"hi"}`}}}},
			want: []planner.Step{wantTool("status-check", map[string]string{"service": "x", "replicas": "3", "force": "true", "note": "hi"})},
		},
		"null-declared-parameter-dropped": {
			// A declared optional parameter sent as null counts as absent.
			msg:  respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-check", Arguments: `{"service":"x","note":null}`}}}},
			want: []planner.Step{wantTool("status-check", map[string]string{"service": "x"})},
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
		"reply-missing-text-key": {
			msg:     respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "reply", Arguments: `{}`}}}},
			wantErr: "reply call has empty text",
		},
		"reply-non-string-text": {
			msg:     respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "reply", Arguments: `{"text":5}`}}}},
			wantErr: "reply call has empty text",
		},
		"all-arguments-dropped": {
			// Every supplied argument is null, so the call carries no arguments.
			msg:  respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-opt", Arguments: `{"opt":null}`}}}},
			want: []planner.Step{wantTool("status-opt", nil)},
		},
		// Argument validation against the descriptor.
		"missing-required-parameter": {
			msg:     respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-check", Arguments: `{}`}}}},
			wantErr: `missing required parameter "service"`,
		},
		"undeclared-parameter": {
			msg:     respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-check", Arguments: `{"service":"x","bogus":"y"}`}}}},
			wantErr: `undeclared parameter "bogus"`,
		},
		"parameter-on-no-arg-tool": {
			msg:     respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-list", Arguments: `{"service":"x"}`}}}},
			wantErr: `undeclared parameter "service"`,
		},
		"wrong-boolean-type": {
			msg:     respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-check", Arguments: `{"service":"x","force":"yes"}`}}}},
			wantErr: `parameter "force": must be a boolean`,
		},
		"wrong-string-type": {
			msg:     respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-check", Arguments: `{"service":"x","note":5}`}}}},
			wantErr: `parameter "note": must be a string`,
		},
		"wrong-number-type": {
			msg:     respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-check", Arguments: `{"service":"x","ratio":"nope"}`}}}},
			wantErr: `parameter "ratio": must be a number`,
		},
		"number-parameter-valid": {
			msg:  respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-check", Arguments: `{"service":"x","ratio":1.5}`}}}},
			want: []planner.Step{wantTool("status-check", map[string]string{"service": "x", "ratio": "1.5"})},
		},
		"untyped-parameter-validated-as-string": {
			msg:  respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-check", Arguments: `{"service":"x","tag":"v1"}`}}}},
			want: []planner.Step{wantTool("status-check", map[string]string{"service": "x", "tag": "v1"})},
		},
		"integer-valued-float": {
			// JSON Schema "integer" is mathematical, so 3.0 is a valid integer
			// and is normalized to canonical decimal for the tool.
			msg:  respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-scale", Arguments: `{"replicas":3.0}`}}}},
			want: []planner.Step{wantTool("status-scale", map[string]string{"replicas": "3"})},
		},
		"integer-in-exponent-form": {
			msg:  respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-scale", Arguments: `{"replicas":1e3}`}}}},
			want: []planner.Step{wantTool("status-scale", map[string]string{"replicas": "1000"})},
		},
		"large-integer-keeps-precision": {
			// Beyond float64's exact range; big.Rat preserves the value.
			msg:  respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-scale", Arguments: `{"replicas":9007199254740993}`}}}},
			want: []planner.Step{wantTool("status-scale", map[string]string{"replicas": "9007199254740993"})},
		},
		"non-integer-value": {
			msg:     respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-scale", Arguments: `{"replicas":2.5}`}}}},
			wantErr: `parameter "replicas": must be an integer`,
		},
		"non-numeric-integer": {
			msg:     respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status-scale", Arguments: `{"replicas":"x"}`}}}},
			wantErr: `parameter "replicas": must be an integer`,
		},
	}

	funcs := map[string]toolFunc{
		"status-check": {scheme: "status-check", params: []tool.Param{
			{Name: "service", Type: "string", Required: true},
			{Name: "verbose", Type: "string"},
			{Name: "replicas", Type: "integer"},
			{Name: "force", Type: "boolean"},
			{Name: "note", Type: "string"},
			{Name: "ratio", Type: "number"},
			{Name: "tag"}, // no type -> validated as string
		}},
		"status-list":  {scheme: "status-list"},
		"status-scale": {scheme: "status-scale", params: []tool.Param{{Name: "replicas", Type: "integer", Required: true}}},
		"status-opt":   {scheme: "status-opt", params: []tool.Param{{Name: "opt", Type: "string"}}},
		"ping":         {scheme: "ping"},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			plan, _, err := stepsFromMessage(tc.msg, funcs)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, plan.Steps)
		})
	}
}

// wantReply is the prose reply step the mapping emits; a prose reply is not a
// provider tool call, so it carries no id.
func wantReply(text string) planner.Step {
	return planner.Step{Tool: reply.URL, Call: tool.Call{
		Arguments: map[string]string{"text": text},
	}}
}

// wantReplyCall is the reply step emitted from a reply function call, which
// retains its (normalized) provider tool-call id.
func wantReplyCall(text, id string) planner.Step {
	return planner.Step{Tool: reply.URL, ID: id, Call: tool.Call{
		Arguments: map[string]string{"text": text},
	}}
}

// wantTool is the feedback step emitted from an operational tool call. Every
// test case has a single tool call at index 0, so its normalized id is "call_0".
func wantTool(scheme string, args map[string]string) planner.Step {
	return planner.Step{Tool: scheme + "://", ID: "call_0", Feedback: true, Call: tool.Call{Arguments: args}}
}
