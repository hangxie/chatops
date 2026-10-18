package agent

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/hangxie/chatops/internal/llm"
)

const (
	// declineTool has no "__", so no MCP tool's <server>__<tool> name can collide with it.
	declineTool = "decline"
	// builtinServer labels the decline step in stats and metrics.
	builtinServer = "chatops"
	// noToolsReply answers while the catalog is empty: an outage, which must not read as an off-topic decline.
	noToolsReply = "No tools are available right now, so I cannot answer. Check that the MCP servers are running."
)

// errNoToolCall fails a turn whose forced first call returned text, which would otherwise be posted unverified.
var errNoToolCall = errors.New("model returned no tool call although one was required")

var declineSpec = llm.ToolSpec{
	Name: declineTool,
	Description: "Decline a message that no other available tool can help with, such as a greeting, thanks, small talk, " +
		"or a request about something these tools do not cover. Never use it when another tool can answer the request.",
	Parameters: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
}

// splitDecline removes decline calls, reporting whether there were any.
func splitDecline(calls []llm.ToolCall) (rest []llm.ToolCall, declined bool) {
	for _, call := range calls {
		if call.Name == declineTool {
			declined = true
			continue
		}
		rest = append(rest, call)
	}
	return rest, declined
}

func declineStep() Step {
	return Step{Phase: PhaseTool, Name: declineTool, Server: builtinServer, Tool: declineTool, Outcome: OutcomeOK}
}

// declineReply shows unresolved failures instead of the off-topic sentence, and names any down server.
func (a *Agent) declineReply(failures *failureLog) string {
	if lines := failures.unresolved(); len(lines) > 0 {
		return withFailures("", lines)
	}
	reply := a.limits.DeclineReply
	if down := a.downServers(); len(down) > 0 {
		reply += "\n\nUnavailable tool servers: " + strings.Join(down, ", ")
	}
	return reply
}

// answerReply treats a text answer matching the decline sentence as a decline, since the model may write it out.
func (a *Agent) answerReply(text string, failures *failureLog) string {
	if strings.Trim(strings.TrimSpace(text), `"`) == strings.TrimSpace(a.limits.DeclineReply) {
		return a.declineReply(failures)
	}
	return withFailures(text, failures.unresolved())
}

func (a *Agent) downServers() []string {
	var down []string
	for _, server := range a.tools.Health() {
		if !server.Up {
			down = append(down, server.ID)
		}
	}
	return down
}
