package mcphost

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tools returns the catalog: every offered tool, with the name callers pass
// to CallTool. The returned slice and its tools are the caller's to read, not
// to modify.
//
// It reads the cached listing rather than querying the servers, so it is
// cheap enough to call per request and cannot fail; servers that report a
// changed tool list refresh the cache on their own, and one that cannot be
// listed contributes nothing rather than making the whole catalog unreadable.
func (h *Host) Tools(_ context.Context) []*mcp.Tool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return append([]*mcp.Tool(nil), h.ordered...)
}

// Generation reports a counter that changes whenever the catalog changes.
// Callers that derive something from the catalog — a planner's function
// definitions, say — can cache it against this value instead of rebuilding
// per request.
func (h *Host) Generation() uint64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.generation
}

// Names returns the offered tool names in lexical order.
func (h *Host) Names() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	names := make([]string, 0, len(h.catalog))
	for name := range h.catalog {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// CallTool invokes the named tool with args and returns its result.
//
// A tool reporting its own failure does so in the result, with IsError set,
// not as an error here: that is the MCP contract, and it is what lets the
// failure be shown to the requester or fed back to the model. An error from
// CallTool means the call could not be made at all — an unknown name, or a
// server that could not be reached.
func (h *Host) CallTool(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error) {
	h.mu.RLock()
	target, ok := h.catalog[name]
	h.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("mcphost: unknown tool %q; offered tools: %s", name, strings.Join(h.Names(), ", "))
	}
	if target.host != nil {
		result, err := target.host.Handler(ctx, args)
		if err != nil {
			return nil, fmt.Errorf("mcphost: call host tool %q: %w", name, err)
		}
		return result, nil
	}
	result, err := target.session.call(ctx, target.name, args)
	if err != nil {
		return nil, fmt.Errorf("mcphost: call tool %q on server %q: %w", name, target.session.name, err)
	}
	return result, nil
}
