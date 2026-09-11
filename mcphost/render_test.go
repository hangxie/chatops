package mcphost_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/mcphost"
)

func Test_Render(t *testing.T) {
	testCases := map[string]struct {
		result *mcp.CallToolResult
		want   string
	}{
		"nil":   {result: nil, want: ""},
		"empty": {result: &mcp.CallToolResult{}, want: ""},
		"text": {
			result: &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}},
			want:   "pong",
		},
		"joined": {
			result: &mcp.CallToolResult{Content: []mcp.Content{
				&mcp.TextContent{Text: "one"},
				&mcp.TextContent{Text: "two"},
			}},
			want: "one\ntwo",
		},
		"blank-text-dropped": {
			result: &mcp.CallToolResult{Content: []mcp.Content{
				&mcp.TextContent{Text: ""},
				&mcp.TextContent{Text: "two"},
			}},
			want: "two",
		},
		"image": {
			result: &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{MIMEType: "image/png"}}},
			want:   "[image: image/png]",
		},
		"image-no-type": {
			result: &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{}}},
			want:   "[image: unknown type]",
		},
		"audio": {
			result: &mcp.CallToolResult{Content: []mcp.Content{&mcp.AudioContent{MIMEType: "audio/wav"}}},
			want:   "[audio: audio/wav]",
		},
		"resource-link-title": {
			result: &mcp.CallToolResult{Content: []mcp.Content{&mcp.ResourceLink{Title: "Runbook", Name: "rb", URI: "file:///rb"}}},
			want:   "[resource: Runbook]",
		},
		"resource-link-uri": {
			result: &mcp.CallToolResult{Content: []mcp.Content{&mcp.ResourceLink{URI: "file:///rb"}}},
			want:   "[resource: file:///rb]",
		},
		"embedded-text": {
			result: &mcp.CallToolResult{Content: []mcp.Content{
				&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "file:///rb", Text: "inline body"}},
			}},
			want: "inline body",
		},
		"embedded-binary": {
			result: &mcp.CallToolResult{Content: []mcp.Content{
				&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "file:///blob", MIMEType: "application/octet-stream"}},
			}},
			want: "[resource: file:///blob]",
		},
		"embedded-nil": {
			result: &mcp.CallToolResult{Content: []mcp.Content{&mcp.EmbeddedResource{}}},
			want:   "[resource]",
		},
		"mixed": {
			result: &mcp.CallToolResult{Content: []mcp.Content{
				&mcp.TextContent{Text: "here it is"},
				&mcp.ImageContent{MIMEType: "image/png"},
			}},
			want: "here it is\n[image: image/png]",
		},
		"unknown-content-is-skipped": {
			result: &mcp.CallToolResult{Content: []mcp.Content{nil, &mcp.TextContent{Text: "kept"}}},
			want:   "kept",
		},
		"resource-link-with-nothing": {
			result: &mcp.CallToolResult{Content: []mcp.Content{&mcp.ResourceLink{}}},
			want:   "[resource: ]",
		},
		"error-text-is-rendered": {
			result: &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: "boom"}},
			},
			want: "boom",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, mcphost.Render(tc.result))
		})
	}
}

func Test_Render_falls_back_to_structured_content(t *testing.T) {
	// The SDK fills in content for a tool with structured output, so a
	// built-in cannot produce this; a server is free to send structured
	// content alone, and answering with silence would be worse than
	// answering with JSON.
	testCases := map[string]struct {
		result *mcp.CallToolResult
		want   string
	}{
		"object": {
			result: &mcp.CallToolResult{StructuredContent: map[string]any{"count": 3}},
			want:   "{\n  \"count\": 3\n}",
		},
		"array": {
			result: &mcp.CallToolResult{StructuredContent: []any{"a", "b"}},
			want:   "[\n  \"a\",\n  \"b\"\n]",
		},
		"scalar": {
			result: &mcp.CallToolResult{StructuredContent: 42},
			want:   "42",
		},
		"nothing at all": {
			result: &mcp.CallToolResult{},
			want:   "",
		},
		"explicit null": {
			result: &mcp.CallToolResult{StructuredContent: nil},
			want:   "",
		},
		"unserializable": {
			result: &mcp.CallToolResult{StructuredContent: make(chan int)},
			want:   "",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, mcphost.Render(tc.result))
		})
	}
}

func Test_Render_prefers_content_over_structured(t *testing.T) {
	// Text content is the tool's own rendering; the fallback is only for a
	// result that offers nothing else.
	result := &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: "3 pods"}},
		StructuredContent: map[string]any{"count": 3},
	}

	require.Equal(t, "3 pods", mcphost.Render(result))
}

func Test_Render_truncates_structured_content(t *testing.T) {
	big := make([]any, 0, 4000)
	for range 4000 {
		big = append(big, "xxxxxxxxxxxxxxxx")
	}
	result := &mcp.CallToolResult{StructuredContent: big}

	rendered := mcphost.Render(result)

	require.LessOrEqual(t, len(rendered), 16<<10)
	require.True(t, strings.HasSuffix(rendered, "… (truncated)"))
}

func Test_Render_truncates_oversized_output(t *testing.T) {
	huge := strings.Repeat("x", 100<<10)
	result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: huge}}}

	rendered := mcphost.Render(result)
	require.Less(t, len(rendered), len(huge))
	require.LessOrEqual(t, len(rendered), 16<<10)
	require.True(t, strings.HasSuffix(rendered, "… (truncated)"))
}

func Test_Render_truncates_on_rune_boundary(t *testing.T) {
	// Multi-byte runes must not be cut in half, or the message posted to chat
	// would not be valid UTF-8. The leading pad shifts where the cut lands,
	// so some offset forces the boundary search to step back.
	for pad := range 4 {
		result := &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.TextContent{Text: strings.Repeat("x", pad) + strings.Repeat("é", 20<<10)},
		}}

		rendered := mcphost.Render(result)
		require.True(t, utf8.ValidString(rendered), "pad %d produced invalid UTF-8", pad)
		require.True(t, strings.HasSuffix(rendered, "… (truncated)"))
	}
}

func Test_Render_keeps_output_at_the_limit(t *testing.T) {
	exact := strings.Repeat("x", 16<<10)
	result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: exact}}}

	require.Equal(t, exact, mcphost.Render(result))
}
