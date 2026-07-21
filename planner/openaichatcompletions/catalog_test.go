package openaichatcompletions

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/planner"
	"github.com/hangxie/chatops/tool"
	"github.com/hangxie/chatops/tool/reply"
)

func Test_toolDefs_offers_reply_and_sorted_schemes(t *testing.T) {
	defs := toolDefs([]string{"status", "ping"})

	names := make([]string, len(defs))
	for i, def := range defs {
		require.Equal(t, "function", def.Type)
		names[i] = def.Function.Name
	}
	// reply is always first, operational tools follow in sorted order.
	require.Equal(t, []string{"reply", "ping", "status"}, names)

	// The reply function's schema requires text; a tool function's
	// schema requires action.
	require.JSONEq(t, string(replyParams), string(defs[0].Function.Parameters))
	require.Contains(t, string(defs[1].Function.Parameters), `"action"`)
	require.Contains(t, defs[1].Function.Description, "ping")
}

func Test_validateSchemes(t *testing.T) {
	testCases := map[string]struct {
		schemes []string
		wantErr string
	}{
		"valid":              {schemes: []string{"ping", "status", "k8s-prod"}},
		"empty":              {schemes: nil},
		"dot-invalid":        {schemes: []string{"service.status"}, wantErr: "cannot be an OpenAI function name"},
		"plus-invalid":       {schemes: []string{"a+b"}, wantErr: "cannot be an OpenAI function name"},
		"too-long":           {schemes: []string{strings.Repeat("a", 65)}, wantErr: "cannot be an OpenAI function name"},
		"max-length-ok":      {schemes: []string{strings.Repeat("a", 64)}},
		"reply-collision":    {schemes: []string{"reply"}, wantErr: "collides with the built-in reply"},
		"one-bad-among-good": {schemes: []string{"ping", "bad.name"}, wantErr: "bad.name"},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			err := validateSchemes(tc.schemes)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func Test_toolDefs_with_no_schemes_still_offers_reply(t *testing.T) {
	defs := toolDefs(nil)
	require.Len(t, defs, 1)
	require.Equal(t, replyFunc, defs[0].Function.Name)
}

func Test_toolDefs_does_not_mutate_input(t *testing.T) {
	schemes := []string{"status", "ping"}
	toolDefs(schemes)
	require.Equal(t, []string{"status", "ping"}, schemes)
}

func Test_stepsFromMessage(t *testing.T) {
	const conv = "C123"
	toolCallStatus := toolCall{
		Type:     "function",
		Function: functionCall{Name: "status", Arguments: `{"action":"check","target":"github","parameters":{"verbose":"true"}}`},
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
		"other-scheme-with-action": {
			msg:  respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "ping", Arguments: `{"action":"ping"}`}}}},
			want: []planner.Step{{Tool: "ping://", Call: tool.Call{Action: "ping"}}},
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
			msg:     respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status", Arguments: "{not json"}}}},
			wantErr: "decode arguments",
		},
		"unavailable-tool": {
			msg:     respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "deploy", Arguments: `{"action":"go"}`}}}},
			wantErr: `unavailable tool "deploy"`,
		},
		"empty-function-name": {
			msg:     respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "", Arguments: `{"action":"go"}`}}}},
			wantErr: "unavailable tool",
		},
		"tool-call-no-arguments": {
			msg:     respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "ping"}}}},
			wantErr: `tool "ping" call has empty action`,
		},
		"tool-call-blank-action": {
			msg:     respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "status", Arguments: `{"action":"  "}`}}}},
			wantErr: "empty action",
		},
		"empty-reply-text": {
			msg:     respMessage{ToolCalls: []toolCall{{Function: functionCall{Name: "reply", Arguments: `{"text":"  "}`}}}},
			wantErr: "reply call has empty text",
		},
	}

	allowed := map[string]struct{}{"status": {}, "ping": {}}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			plan, err := stepsFromMessage(tc.msg, conv, allowed)
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

func Test_replyParams_is_valid_json(t *testing.T) {
	var v any
	require.NoError(t, json.Unmarshal(replyParams, &v))
	require.NoError(t, json.Unmarshal(toolParams, &v))
}

func Test_mustJSON_panics_on_unmarshalable_value(t *testing.T) {
	// A channel cannot be marshaled, exercising the panic path used to
	// catch a malformed static schema at startup.
	require.PanicsWithValue(t, "openai: marshal static schema: json: unsupported type: chan int", func() {
		mustJSON(make(chan int))
	})
}
