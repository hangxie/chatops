package openaichatcompletions

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hangxie/chatops/planner"
	"github.com/hangxie/chatops/tool/reply"
)

// stepsFromMessage maps one assistant message to plan steps: prose becomes a
// reply step, and each tool call becomes a step invoking that tool.
//
// Model output is untrusted, so a call naming a function that was not offered
// is an error rather than a step. Argument values are not checked here: they
// are the tool's own schema to enforce, and the MCP server validates them on
// the call, reporting a violation as a tool error the requester can see.
func stepsFromMessage(msg respMessage, offered map[string]bool) (planner.Plan, error) {
	var steps []planner.Step
	if text := strings.TrimSpace(msg.Content); text != "" {
		steps = append(steps, replyStep(text))
	}
	for _, call := range msg.ToolCalls {
		name := call.Function.Name
		if !offered[name] {
			return planner.Plan{}, fmt.Errorf("openai: completion called unavailable function %q", name)
		}
		args, err := decodeArguments(call.Function.Arguments)
		if err != nil {
			return planner.Plan{}, fmt.Errorf("openai: decode arguments for %q: %w", name, err)
		}
		steps = append(steps, planner.Step{Tool: name, Arguments: args})
	}
	return planner.Plan{Steps: steps}, nil
}

// decodeArguments decodes a tool call's JSON arguments. An absent or empty
// argument string means the model called the tool with nothing, which is
// valid for a tool that reads nothing.
func decodeArguments(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, err
	}
	return args, nil
}

// replyStep is a step posting text back to the requester through the reply
// tool, mirroring the shape the ping planner emits. The target conversation
// is supplied by the executor, so the step carries only the text.
func replyStep(text string) planner.Step {
	return planner.Step{Tool: reply.ToolName, Arguments: map[string]any{"text": text}}
}
