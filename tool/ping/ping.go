// Package ping implements a dummy MCP tool that always answers "pong",
// useful as a liveness check and as the reference implementation of a
// chatops built-in tool.
//
// The package exports GroupName and Register for wiring the tool into an
// mcpserve.Registry. The group takes no options and no credentials.
//
// The tool takes no arguments.
package ping

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hangxie/chatops/mcpserve"
)

// GroupName is the built-in group this package registers into.
const GroupName = "ping"

// ToolName is the model-facing name of the tool.
const ToolName = "ping"

// Args is the tool's input schema: the ping tool reads nothing.
type Args struct{}

// Register adds the ping tool to s. It takes no options and no credentials.
func Register(s *mcp.Server, opts mcpserve.Options) error {
	if err := mcpserve.CheckOptions(GroupName, opts.Query); err != nil {
		return err
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        ToolName,
		Description: `Liveness check; replies "pong" to confirm the bot is responsive.`,
	}, handle)
	return nil
}

// handle answers "pong". Arguments are ignored.
func handle(ctx context.Context, _ *mcp.CallToolRequest, _ Args) (*mcp.CallToolResult, any, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, fmt.Errorf("ping: %w", err)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil, nil
}
