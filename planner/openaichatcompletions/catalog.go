package openaichatcompletions

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/hangxie/chatops/planner"
	"github.com/hangxie/chatops/tool"
	"github.com/hangxie/chatops/tool/reply"
)

// replyFunc is the built-in function the model calls to reply to the
// requester; it is always offered, alongside the operational tools.
const replyFunc = "reply"

// Descriptions the model sees for the offered functions.
const (
	replyFuncDesc = "Post a message back to the person who sent the request, " +
		"for answers, clarifying questions, or acknowledgements."
	toolFuncDescFmt = "Invoke the %q operational tool. Set action to the verb to " +
		"perform, target to what it applies to (may be empty), and parameters to " +
		"any additional key/value arguments."
)

// JSON Schemas for the two function shapes: reply takes only text, and
// every operational tool takes the generic action/target/parameters
// triple, since tools do not describe their own vocabulary.
var (
	replyParams = mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{
				"type":        "string",
				"description": "The message text to post back to the requester.",
			},
		},
		"required":             []string{"text"},
		"additionalProperties": false,
	})
	toolParams = mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{"type": "string"},
			"target": map[string]any{"type": "string"},
			"parameters": map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "string"},
			},
		},
		"required":             []string{"action"},
		"additionalProperties": false,
	})
)

// maxFuncNameLen is the OpenAI limit on function names.
const maxFuncNameLen = 64

// funcNameRE is the character set OpenAI allows in a function name;
// the tool registry's scheme syntax is broader (it permits "+" and ".").
var funcNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// validateSchemes rejects enabled tool schemes that cannot be offered to
// the model as function names, so an incompatible scheme fails when the
// planner is opened rather than making every completion request fail.
func validateSchemes(schemes []string) error {
	for _, scheme := range schemes {
		if scheme == replyFunc {
			return fmt.Errorf("openai: tool scheme %q collides with the built-in reply function", scheme)
		}
		if len(scheme) > maxFuncNameLen || !funcNameRE.MatchString(scheme) {
			return fmt.Errorf("openai: tool scheme %q cannot be an OpenAI function name (allowed: letters, digits, '_', '-'; max %d characters)", scheme, maxFuncNameLen)
		}
	}
	return nil
}

// toolDefs builds the function catalog offered to the model: the
// built-in reply function plus one generic function per enabled tool
// scheme, in a stable order (reply first, then schemes sorted).
func toolDefs(schemes []string) []toolDef {
	defs := make([]toolDef, 0, len(schemes)+1)
	defs = append(defs, toolDef{Type: "function", Function: functionDef{
		Name:        replyFunc,
		Description: replyFuncDesc,
		Parameters:  replyParams,
	}})
	sorted := append([]string(nil), schemes...)
	sort.Strings(sorted)
	for _, scheme := range sorted {
		defs = append(defs, toolDef{Type: "function", Function: functionDef{
			Name:        scheme,
			Description: fmt.Sprintf(toolFuncDescFmt, scheme),
			Parameters:  toolParams,
		}})
	}
	return defs
}

// callArgs is the union of argument shapes the model may send: text for
// the reply function, and action/target/parameters for an operational
// tool.
type callArgs struct {
	Text       string            `json:"text"`
	Action     string            `json:"action"`
	Target     string            `json:"target"`
	Parameters map[string]string `json:"parameters"`
}

// stepsFromMessage maps one assistant message to plan steps: prose and
// reply calls become reply steps into conv, other tool calls become
// "<scheme>://" steps. Model output is untrusted, so each tool call is
// checked against allowed (the offered schemes) and its required
// text/action validated; it errors on bad JSON, an unavailable tool, or
// a missing text/action.
func stepsFromMessage(msg respMessage, conv string, allowed map[string]struct{}) (planner.Plan, error) {
	var steps []planner.Step
	if text := strings.TrimSpace(msg.Content); text != "" {
		steps = append(steps, replyStep(conv, text, nil))
	}
	for _, call := range msg.ToolCalls {
		var args callArgs
		if raw := call.Function.Arguments; raw != "" {
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				return planner.Plan{}, fmt.Errorf("openai: decode arguments for %q: %w", call.Function.Name, err)
			}
		}
		name := call.Function.Name
		if name == replyFunc {
			if strings.TrimSpace(args.Text) == "" {
				return planner.Plan{}, fmt.Errorf("openai: reply call has empty text")
			}
			steps = append(steps, replyStep(conv, args.Text, nil))
			continue
		}
		if _, ok := allowed[name]; !ok {
			return planner.Plan{}, fmt.Errorf("openai: completion called unavailable tool %q", name)
		}
		if strings.TrimSpace(args.Action) == "" {
			return planner.Plan{}, fmt.Errorf("openai: tool %q call has empty action", name)
		}
		steps = append(steps, planner.Step{
			Tool: name + "://",
			Call: tool.Call{Action: args.Action, Target: args.Target, Parameters: args.Parameters},
		})
	}
	return planner.Plan{Steps: steps}, nil
}

// replyStep is a step posting text back into conversation conv through
// the reply tool, mirroring the shape the ping planner emits.
func replyStep(conv, text string, choices []tool.Choice) planner.Step {
	return planner.Step{Tool: reply.URL, Call: tool.Call{
		Action:     "send",
		Target:     conv,
		Parameters: map[string]string{"text": text},
		Choices:    choices,
	}}
}

// mustJSON marshals a static schema value, panicking on failure (a
// programmer error caught at startup).
func mustJSON(v any) json.RawMessage {
	buf, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("openai: marshal static schema: %v", err))
	}
	return buf
}
