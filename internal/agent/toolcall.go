package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/hangxie/chatops/internal/llm"
	"github.com/hangxie/chatops/internal/mcp"
)

// runTool runs one call and returns text for the model; failures become "error: ..." text.
func (a *Agent) runTool(ctx context.Context, catalog *mcp.Catalog, call llm.ToolCall) string {
	tool, ok := catalog.Lookup(call.Name)
	if !ok {
		return fmt.Sprintf("error: unknown tool %q", call.Name)
	}
	args := json.RawMessage(call.Arguments)
	if err := tool.Validate(args); err != nil {
		return "error: invalid arguments: " + err.Error()
	}

	callCtx, cancel := context.WithTimeout(ctx, a.limits.ToolTimeout)
	defer cancel()
	start := time.Now()
	result, err := a.tools.Call(callCtx, tool, args)
	log := a.logger.With("server", tool.Server, "tool", tool.MCPName, "duration", time.Since(start))
	if err != nil && callCtx.Err() != nil && ctx.Err() == nil {
		log.Warn("tool call timed out", "error", err)
		return fmt.Sprintf("error: tool call timed out after %s", a.limits.ToolTimeout)
	}
	if err != nil {
		log.Warn("tool call failed", "error", err)
		return "error: tool call failed: " + err.Error()
	}
	log.Info("tool call completed", "is_error", result.IsError, "bytes", len(result.Text))

	text := truncate(result.Text, a.limits.MaxToolResultBytes)
	if result.IsError {
		return "error: " + text
	}
	return text
}

// truncate cuts text to limit bytes on a rune boundary, then appends a marker beyond the limit.
func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := max(limit, 0)
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return fmt.Sprintf("%s\n[truncated: showing %d of %d bytes]", text[:cut], cut, len(text))
}
