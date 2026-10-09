// Package ping provides a harmless connectivity tool for smoke tests.
package ping

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Output is the structured result of the ping tool.
type Output struct {
	Reply string `json:"reply" jsonschema:"always pong"`
}

// Register adds the ping tool to server.
func Register(server *mcp.Server) {
	openWorld := false
	mcp.AddTool(server, &mcp.Tool{
		Name:        "ping",
		Description: "Check that the MCP server is reachable. Takes no arguments and replies pong.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
			OpenWorldHint:  &openWorld,
		},
	}, handle)
}

func handle(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, Output, error) {
	result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}
	return result, Output{Reply: "pong"}, nil
}
