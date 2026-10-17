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

// maxLoggedArgumentBytes caps the arguments a debug log line repeats.
const maxLoggedArgumentBytes = 1024

// runTool returns text for the model, its step, and a failure line for the user, empty for errors the model must fix.
func (a *Agent) runTool(ctx context.Context, catalog *mcp.Catalog, call llm.ToolCall) (string, Step, string) {
	// Arguments are model output that may echo user text, so they are logged only at debug level.
	a.logger.Debug("tool call requested", "tool", call.Name, "arguments", truncate(call.Arguments, maxLoggedArgumentBytes))
	step := Step{Phase: PhaseTool, Name: call.Name, Outcome: OutcomeError}
	start := time.Now()
	content, failure := a.callTool(ctx, catalog, call, &step)
	step.Duration, step.SentBytes = time.Since(start), len(content)
	return content, step, failure
}

func (a *Agent) callTool(ctx context.Context, catalog *mcp.Catalog, call llm.ToolCall, step *Step) (content, failure string) {
	tool, ok := catalog.Lookup(call.Name)
	if !ok {
		a.logger.Warn("tool call rejected", "tool", call.Name, "reason", "unknown tool")
		return fmt.Sprintf("error: unknown tool %q", call.Name), ""
	}
	step.Server, step.Tool = tool.Server, tool.MCPName
	args := json.RawMessage(call.Arguments)
	if err := tool.Validate(args); err != nil {
		// The error can quote argument values, so it is logged only with them at debug level.
		a.logger.Warn("tool call rejected", "tool", call.Name, "reason", "invalid arguments")
		a.logger.Debug("tool call arguments invalid", "tool", call.Name, "error", err)
		return "error: invalid arguments: " + err.Error(), ""
	}

	callCtx, cancel := context.WithTimeout(ctx, a.limits.ToolTimeout)
	defer cancel()
	start := time.Now()
	result, err := a.tools.Call(callCtx, tool, args)
	log := a.logger.With("server", tool.Server, "tool", tool.MCPName, "duration", time.Since(start))
	// Servers may ignore ctx, so a result after the deadline is discarded even on success.
	if callCtx.Err() != nil {
		step.Outcome = errorOutcome(callCtx.Err())
		log.Warn("tool call timed out", "error", err)
		failure = fmt.Sprintf("tool call timed out after %s", a.limits.ToolTimeout)
		return "error: " + failure, failure
	}
	if err != nil {
		log.Warn("tool call failed", "error", err)
		failure = "tool call failed: " + err.Error()
		return "error: " + failure, failure
	}
	log.Info("tool call completed", "is_error", result.IsError, "bytes", len(result.Text))

	step.ResultBytes, step.Truncated = len(result.Text), len(result.Text) > a.limits.MaxToolResultBytes
	text := truncate(result.Text, a.limits.MaxToolResultBytes)
	if result.IsError {
		return "error: " + text, result.Text
	}
	step.Outcome = OutcomeOK
	return text, ""
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
