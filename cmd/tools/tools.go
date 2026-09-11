// Package tools lists the operational tools the binary can offer.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hangxie/chatops/internal/builtin"
	"github.com/hangxie/chatops/mcphost"
	"github.com/hangxie/chatops/tool/reply"
)

// Cmd is the kong command for tools.
type Cmd struct {
	Builtin []string `name:"builtin" help:"Built-in tool group to list, as a bare name (k8s) or a name with options (k8s?context=prod); repeat for several groups (default: all)."`
	Tools   []string `name:"tool" help:"Tool name to list, with '*' and '?' wildcards (k8s-*); repeat to list several (default: all). Mirrors the server's --tool."`
	JSON    bool     `short:"j" help:"Output in JSON format." default:"false"`
}

// listing is one tool as reported by --json.
type listing struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Run prints the offered tools, one per line, or as a JSON array when --json
// is set.
//
// The tools are listed by connecting the built-in MCP servers and asking them,
// rather than from a compiled-in table, so what is listed is exactly what a
// planner would be offered — including the effect of --builtin and --tool,
// which mean the same here as they do on the server.
func (c Cmd) Run(ctx context.Context) (err error) {
	// Listing never calls a tool, but a group may still report a problem
	// while registering; stdout carries the listing, so that goes to stderr.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	servers, err := builtin.Servers(c.Builtin, nil, logger)
	if err != nil {
		return fmt.Errorf("tools: %w", err)
	}
	host, err := mcphost.New(ctx, mcphost.Config{
		Servers: servers,
		// The reply tool needs a live chat connection to post into, so it is
		// listed from its definition rather than opened.
		HostTools: []mcphost.HostTool{{Def: reply.Definition(), Handler: unavailable}},
		Allow:     c.Tools,
		// This is the command an operator reaches for to try a --tool
		// pattern out, so the warning about one that matches nothing has to
		// arrive here of all places.
		Logger: logger,
	})
	if err != nil {
		return fmt.Errorf("tools: %w", err)
	}
	defer func() {
		err = errors.Join(err, host.Close())
	}()

	tools := host.Tools(ctx)
	if c.JSON {
		printJSON(tools)
		return nil
	}
	for _, t := range tools {
		fmt.Println(t.Name)
	}
	return nil
}

// printJSON writes the tools as a JSON array of name and description.
func printJSON(tools []*mcp.Tool) {
	listings := make([]listing, 0, len(tools))
	for _, t := range tools {
		listings = append(listings, listing{Name: t.Name, Description: t.Description})
	}
	// The listings hold only strings, so marshalling cannot fail.
	buf, _ := json.Marshal(listings)
	fmt.Println(string(buf))
}

// unavailable stands in for a host tool that cannot run in this command.
// Listing never calls a tool, so it is never reached.
func unavailable(context.Context, map[string]any) (*mcp.CallToolResult, error) {
	return nil, errors.New("tools: listing does not invoke tools")
}
