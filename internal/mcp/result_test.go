package mcp

import (
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func Test_newResult(t *testing.T) {
	tests := map[string]struct {
		in   *mcpsdk.CallToolResult
		want Result
	}{
		"text": {
			in:   &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "a"}, &mcpsdk.TextContent{Text: "b"}}},
			want: Result{Text: "a\nb"},
		},
		"error": {
			in:   &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "boom"}}, IsError: true},
			want: Result{Text: "boom", IsError: true},
		},
		"structured-only": {
			in:   &mcpsdk.CallToolResult{StructuredContent: map[string]any{"reply": "pong"}},
			want: Result{Text: `{"reply":"pong"}`},
		},
		"text-wins-over-structured": {
			in:   &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "pong"}}, StructuredContent: map[string]any{"reply": "pong"}},
			want: Result{Text: "pong"},
		},
		"structured-unmarshalable": {
			in:   &mcpsdk.CallToolResult{StructuredContent: func() {}},
			want: Result{Text: "[structured content could not be encoded]"},
		},
		"media-and-resources": {
			in: &mcpsdk.CallToolResult{Content: []mcpsdk.Content{
				&mcpsdk.ImageContent{MIMEType: "image/png"},
				&mcpsdk.AudioContent{MIMEType: "audio/wav"},
				&mcpsdk.ResourceLink{URI: "file:///a"},
				&mcpsdk.EmbeddedResource{Resource: &mcpsdk.ResourceContents{URI: "file:///b", Text: "inline"}},
				&mcpsdk.EmbeddedResource{Resource: &mcpsdk.ResourceContents{URI: "file:///c", Blob: []byte{1}}},
				&mcpsdk.EmbeddedResource{},
			}},
			want: Result{Text: "[image/png image omitted]\n[audio/wav audio omitted]\n[resource link: file:///a]\ninline\n[resource file:///c omitted]\n[resource omitted]"},
		},
		"empty": {
			in:   &mcpsdk.CallToolResult{},
			want: Result{Text: ""},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, newResult(tc.in))
		})
	}
}
