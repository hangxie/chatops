package llm

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/config"
)

func ptr[T any](v T) *T { return &v }

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
		sampling        config.Sampling
		want            string
	}{
		"plain": {
			want: `{
				"model": "m",
				"stream": true, "stream_options": {"include_usage": true},
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
				"stream": true, "stream_options": {"include_usage": true},
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
		"sampling": {
			sampling: config.Sampling{
				Temperature:       ptr(0.0),
				TopP:              ptr(0.9),
				TopK:              ptr(40),
				MinP:              ptr(0.05),
				MaxTokens:         ptr(512),
				FrequencyPenalty:  ptr(0.1),
				PresencePenalty:   ptr(-0.1),
				RepetitionPenalty: ptr(1.1),
				Seed:              ptr(7),
				Stop:              []string{"\n\n"},
			},
			want: `{
				"model": "m",
				"stream": true, "stream_options": {"include_usage": true},
				"messages": [
					{"role":"system","content":"be brief"},
					{"role":"user","content":"ping it"},
					{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"builtin__ping","arguments":"{}"}}]},
					{"role":"tool","content":"pong","tool_call_id":"c1"},
					{"role":"assistant","content":"It replied pong."}
				],
				"tools": [{"type":"function","function":{"name":"builtin__ping","description":"Ping.","parameters":{"type":"object"}}}],
				"temperature": 0, "top_p": 0.9, "top_k": 40, "min_p": 0.05, "max_tokens": 512,
				"frequency_penalty": 0.1, "presence_penalty": -0.1, "repetition_penalty": 1.1,
				"seed": 7, "stop": ["\n\n"]
			}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(toWireRequest(config.Model{Model: "m", DisableThinking: tc.disableThinking, Sampling: tc.sampling}, req))
			require.NoError(t, err)
			require.JSONEq(t, tc.want, string(encoded))
		})
	}
}

func Test_toWireRequest_no_tools(t *testing.T) {
	// tool_choice is dropped without tools, which endpoints would reject.
	req := Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}, ToolChoice: ToolChoiceRequired}
	encoded, err := json.Marshal(toWireRequest(config.Model{Model: "m"}, req))
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"m","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`, string(encoded))
}

func Test_toWireRequest_stream_off(t *testing.T) {
	// Both fields are omitted, since endpoints that mishandle streaming may also reject stream_options.
	req := Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}}
	encoded, err := json.Marshal(toWireRequest(config.Model{Model: "m", Stream: streamOff()}, req))
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, string(encoded))
}

func Test_toWireRequest_tool_choice(t *testing.T) {
	tools := []ToolSpec{{Name: "t", Parameters: json.RawMessage(`{"type":"object"}`)}}
	tests := map[string]struct {
		choice string
		want   any
	}{
		"default omitted": {choice: "", want: nil},
		"required":        {choice: ToolChoiceRequired, want: "required"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(toWireRequest(config.Model{Model: "m"}, Request{Tools: tools, ToolChoice: tc.choice}))
			require.NoError(t, err)
			var got map[string]any
			require.NoError(t, json.Unmarshal(encoded, &got))
			require.Equal(t, tc.want, got["tool_choice"])
		})
	}
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

func Test_wireResponse_usage(t *testing.T) {
	tests := map[string]struct {
		body string
		want *Usage
	}{
		"reported": {body: `{"usage":{"prompt_tokens":1200,"completion_tokens":45,"total_tokens":1245}}`, want: &Usage{PromptTokens: 1200, CompletionTokens: 45}},
		"zero":     {body: `{"usage":{"prompt_tokens":0,"completion_tokens":0}}`, want: &Usage{}},
		"absent":   {body: `{}`},
		"null":     {body: `{"usage":null}`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var resp wireResponse
			require.NoError(t, json.Unmarshal([]byte(tc.body), &resp))
			require.Equal(t, tc.want, resp.usage())
		})
	}
}
