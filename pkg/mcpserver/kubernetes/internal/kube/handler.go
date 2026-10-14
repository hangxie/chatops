package kube

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ReadOnly is the annotation set every tool in this group carries.
func ReadOnly() *mcp.ToolAnnotations {
	openWorld := true
	return &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &openWorld}
}

// PerTarget builds a handler that validates targets, fetches them in parallel, and renders one block per target.
func PerTarget[In any, K comparable, T, Out any](
	targetsOf func(In) ([]K, error),
	fetch func(context.Context, K) (T, error),
	format func(T) string,
	wrap func([]T) Out,
) mcp.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		var zero Out
		keys, err := targetsOf(in)
		if err != nil {
			return nil, zero, err
		}
		items, err := FetchAll(ctx, keys, fetch)
		if err != nil {
			return nil, zero, err
		}
		return TextResult(formatEach(items, format)), wrap(items), nil
	}
}

// TextResult wraps text as the tool's human-readable content.
func TextResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}
