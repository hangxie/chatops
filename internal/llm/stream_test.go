package llm

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_readStream(t *testing.T) {
	tests := map[string]struct {
		body   string
		want   Message
		usage  *Usage
		errMsg string
	}{
		"text": {
			body: `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}

data: {"choices":[{"index":0,"delta":{"content":"hel"}}]}

data: {"choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":"stop"}]}

data: {"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":2}}

data: [DONE]

`,
			want:  Message{Role: RoleAssistant, Content: "hello"},
			usage: &Usage{PromptTokens: 7, CompletionTokens: 2},
		},
		"tool-calls": {
			body: `data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"x__y","arguments":""}}]}}]}
data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"n\":"}}]}}]}
data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}}]}
data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"b","type":"function","function":{"name":"x__z"}}]}}]}
data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}
data: [DONE]
`,
			want: Message{Role: RoleAssistant, ToolCalls: []ToolCall{
				{ID: "a", Name: "x__y", Arguments: `{"n":1}`},
				{ID: "b", Name: "x__z", Arguments: `{}`},
			}},
		},
		"repeated-id-and-name": {
			body: `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"x__y","arguments":"{"}}]}}]}
data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"x__y","arguments":"}"}}]}}]}
data: [DONE]
`,
			want: Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "a", Name: "x__y", Arguments: `{}`}}},
		},
		"comments-crlf-and-no-space": {
			body: ": keep-alive\r\nevent: message\r\ndata:{\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\r\n\r\ndata: [DONE]\r\n",
			want: Message{Role: RoleAssistant, Content: "hi"},
		},
		"think-stripped": {
			body: `data: {"choices":[{"delta":{"content":"<think>x</think>"}}]}
data: {"choices":[{"delta":{"content":" hi"},"finish_reason":"stop"}]}
`,
			want: Message{Role: RoleAssistant, Content: "hi"},
		},
		"finish-without-done": {
			body: `data: {"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}]}
`,
			want: Message{Role: RoleAssistant, Content: "hi"},
		},
		"truncated": {
			body: `data: {"choices":[{"delta":{"content":"hi"}}]}
`,
			errMsg: "stream ended before the response finished",
		},
		"finish-length": {
			body: `data: {"choices":[{"delta":{"content":"hi"},"finish_reason":"length"}]}
data: [DONE]
`,
			errMsg: "response truncated at the token limit (finish_reason length)",
		},
		"tool-call-index-gap": {
			body:   `data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"b","function":{"name":"x__z"}}]}}]}` + "\n",
			errMsg: "stream tool call index 1 out of order",
		},
		"error-event": {
			body: `data: {"error":{"message":"model crashed"}}
`,
			errMsg: "stream error: model crashed",
		},
		"bad-json": {
			body:   "data: {not json\n",
			errMsg: "decode stream chunk",
		},
		"empty": {
			body:   "data: [DONE]\n",
			errMsg: "response has neither content nor tool calls",
		},
		"oversized-content": {
			body:   `data: {"choices":[{"delta":{"content":"` + strings.Repeat("a", maxResponseBytes/2) + `"}}]}` + "\n" + `data: {"choices":[{"delta":{"content":"` + strings.Repeat("a", maxResponseBytes/2+1) + `"}}]}` + "\n",
			errMsg: "completion response exceeds 1048576 bytes",
		},
		"oversized-line": {
			body:   "data: " + strings.Repeat("a", maxResponseBytes+1) + "\n",
			errMsg: "read completion stream",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			wire, err := readStream(strings.NewReader(tc.body))
			if err == nil {
				var msg Message
				msg, err = fromWireResponse(wire)
				if err == nil {
					require.Empty(t, tc.errMsg)
					require.Equal(t, tc.want, msg)
					require.Equal(t, tc.usage, wire.usage())
					return
				}
			}
			require.NotEmpty(t, tc.errMsg, "unexpected error: %v", err)
			require.ErrorContains(t, err, tc.errMsg)
		})
	}
}
