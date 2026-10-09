package llm

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_toWireRequest(t *testing.T) {
	req := Request{
		Messages: []Message{
			{Role: RoleSystem, Content: "be brief"},
			{Role: RoleUser, Content: "ping it"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "builtin__ping", Arguments: `{}`}}},
			{Role: RoleTool, ToolCallID: "c1", Content: "pong"},
			{Role: RoleAssistant, Content: "It replied pong."},
		},
		Tools: []ToolSpec{{Name: "builtin__ping", Description: "Ping.", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}

	tests := map[string]struct {
		disableThinking bool
		want            string
	}{
		"plain": {
			want: `{
				"model": "m",
				"messages": [
					{"role":"system","content":"be brief"},
					{"role":"user","content":"ping it"},
					{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"builtin__ping","arguments":"{}"}}]},
					{"role":"tool","content":"pong","tool_call_id":"c1"},
					{"role":"assistant","content":"It replied pong."}
				],
				"tools": [{"type":"function","function":{"name":"builtin__ping","description":"Ping.","parameters":{"type":"object"}}}]
			}`,
		},
		"no-think": {
			disableThinking: true,
			want: `{
				"model": "m",
				"messages": [
					{"role":"system","content":"be brief"},
					{"role":"user","content":"ping it"},
					{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"builtin__ping","arguments":"{}"}}]},
					{"role":"tool","content":"pong","tool_call_id":"c1"},
					{"role":"assistant","content":"It replied pong."}
				],
				"tools": [{"type":"function","function":{"name":"builtin__ping","description":"Ping.","parameters":{"type":"object"}}}],
				"reasoning_effort": "none",
				"chat_template_kwargs": {"enable_thinking": false}
			}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(toWireRequest("m", tc.disableThinking, req))
			require.NoError(t, err)
			require.JSONEq(t, tc.want, string(encoded))
		})
	}
}

func Test_toWireRequest_no_tools(t *testing.T) {
	encoded, err := json.Marshal(toWireRequest("m", false, Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}}))
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, string(encoded))
}

func Test_fromWireResponse(t *testing.T) {
	tests := map[string]struct {
		body   string
		want   Message
		errMsg string
	}{
		"text": {
			body: `{"choices":[{"message":{"role":"assistant","content":"hello"}}]}`,
			want: Message{Role: RoleAssistant, Content: "hello"},
		},
		"think-stripped": {
			body: `{"choices":[{"message":{"role":"assistant","content":"<think>\nhmm\n</think>\n\nhello"}}]}`,
			want: Message{Role: RoleAssistant, Content: "hello"},
		},
		"empty-think-stripped": {
			body: `{"choices":[{"message":{"role":"assistant","content":"  <think>\n\n</think>hello  "}}]}`,
			want: Message{Role: RoleAssistant, Content: "hello"},
		},
		"unclosed-think-kept": {
			body: `{"choices":[{"message":{"role":"assistant","content":"<think> partial"}}]}`,
			want: Message{Role: RoleAssistant, Content: "<think> partial"},
		},
		"tool-calls": {
			body: `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[
				{"id":"a","type":"function","function":{"name":"x__y","arguments":"{\"n\":1}"}},
				{"type":"function","function":{"name":"x__z","arguments":""}}
			]}}]}`,
			want: Message{Role: RoleAssistant, ToolCalls: []ToolCall{
				{ID: "a", Name: "x__y", Arguments: `{"n":1}`},
				{ID: "call_1", Name: "x__z", Arguments: `{}`},
			}},
		},
		"finish-stop": {
			body: `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"hello"}}]}`,
			want: Message{Role: RoleAssistant, Content: "hello"},
		},
		"finish-tool-calls": {
			body: `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"x__y","arguments":"{}"}}]}}]}`,
			want: Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "a", Name: "x__y", Arguments: `{}`}}},
		},
		"finish-length": {
			body:   `{"choices":[{"finish_reason":"length","message":{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"x__y","arguments":"{\"n\":"}}]}}]}`,
			errMsg: "response truncated at the token limit (finish_reason length)",
		},
		"finish-content-filter": {
			body:   `{"choices":[{"finish_reason":"content_filter","message":{"role":"assistant","content":"partial"}}]}`,
			errMsg: "response withheld by a content filter (finish_reason content_filter)",
		},
		"no-choices": {
			body:   `{"choices":[]}`,
			errMsg: "response has no choices",
		},
		"empty-answer": {
			body:   `{"choices":[{"message":{"role":"assistant","content":"<think></think>"}}]}`,
			errMsg: "response has neither content nor tool calls",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var resp wireResponse
			require.NoError(t, json.Unmarshal([]byte(tc.body), &resp))
			got, err := fromWireResponse(resp)
			if tc.errMsg != "" {
				require.EqualError(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
