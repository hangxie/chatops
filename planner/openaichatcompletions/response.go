package openaichatcompletions

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hangxie/chatops/planner"
	"github.com/hangxie/chatops/tool"
	"github.com/hangxie/chatops/tool/reply"
)

// callArgs is the union of argument shapes the model may send: text for
// reply, target/parameters for a tool (the action is in the function name).
// Parameters decode as raw JSON since a typed function may declare
// non-string values; they are stringified later (see paramsToStrings).
type callArgs struct {
	Text       string                     `json:"text"`
	Target     string                     `json:"target"`
	Parameters map[string]json.RawMessage `json:"parameters"`
}

// stepsFromMessage maps one assistant message to plan steps: prose and reply
// calls become reply steps into conv, other tool calls become "<scheme>://"
// steps. Model output is untrusted, so each call is resolved via funcs and
// validated before a step is produced. It errors on bad JSON, an unavailable
// function, empty reply text, or invalid arguments.
func stepsFromMessage(msg respMessage, conv string, funcs map[string]toolFunc) (planner.Plan, error) {
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
		tf, ok := funcs[name]
		if !ok {
			return planner.Plan{}, fmt.Errorf("openai: completion called unavailable function %q", name)
		}
		if err := validateArgs(name, tf.action, args); err != nil {
			return planner.Plan{}, err
		}
		steps = append(steps, planner.Step{
			Tool: tf.scheme + "://",
			// Trim so what validateArgs checked is what the tool receives.
			Call: tool.Call{Action: tf.action.Name, Target: strings.TrimSpace(args.Target), Parameters: paramsToStrings(args.Parameters)},
		})
	}
	return planner.Plan{Steps: steps}, nil
}

// validateArgs rejects a tool call whose arguments violate the action's
// schema: a missing/forbidden target, a missing-required or undeclared
// parameter, or a wrong scalar type. JSON null and empty values count as
// absent, matching paramsToStrings.
func validateArgs(fn string, a tool.Action, args callArgs) error {
	if a.TakesTarget {
		if strings.TrimSpace(args.Target) == "" {
			return fmt.Errorf("openai: function %q requires a target", fn)
		}
	} else if strings.TrimSpace(args.Target) != "" {
		return fmt.Errorf("openai: function %q does not take a target", fn)
	}

	declared := make(map[string]tool.Param, len(a.Parameters))
	for _, p := range a.Parameters {
		declared[p.Name] = p
	}
	for name := range args.Parameters {
		if _, ok := declared[name]; !ok {
			return fmt.Errorf("openai: function %q got undeclared parameter %q", fn, name)
		}
	}
	for _, p := range a.Parameters {
		raw, present := args.Parameters[p.Name]
		if present && argAbsent(raw) {
			present = false
		}
		if !present {
			if p.Required {
				return fmt.Errorf("openai: function %q missing required parameter %q", fn, p.Name)
			}
			continue
		}
		if err := checkScalarType(p, raw); err != nil {
			return fmt.Errorf("openai: function %q parameter %q: %w", fn, p.Name, err)
		}
	}
	return nil
}

// argAbsent reports whether a raw JSON parameter value should be treated as
// not supplied: JSON null or an empty token, the same values paramsToStrings
// drops.
func argAbsent(raw json.RawMessage) bool {
	t := strings.TrimSpace(string(raw))
	return t == "" || t == "null"
}

// checkScalarType verifies raw matches the parameter's declared scalar type
// ("string", "number", "integer", or "boolean"; empty means "string").
func checkScalarType(p tool.Param, raw json.RawMessage) error {
	typ := p.Type
	if typ == "" {
		typ = "string"
	}
	switch typ {
	case "string":
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return errors.New("must be a string")
		}
	case "boolean":
		var b bool
		if json.Unmarshal(raw, &b) != nil {
			return errors.New("must be a boolean")
		}
	case "number":
		var f float64
		if json.Unmarshal(raw, &f) != nil {
			return errors.New("must be a number")
		}
	case "integer":
		var f float64
		if json.Unmarshal(raw, &f) != nil || strings.ContainsAny(strings.TrimSpace(string(raw)), ".eE") {
			return errors.New("must be an integer")
		}
	}
	return nil
}

// paramsToStrings converts raw JSON parameter values into the string map
// tool.Call carries: a JSON string yields its value, other scalars their
// literal text. Null/empty are dropped, and it returns nil when none remain.
func paramsToStrings(raw map[string]json.RawMessage) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]string, len(raw))
	for key, value := range raw {
		text := strings.TrimSpace(string(value))
		if text == "" || text == "null" {
			continue
		}
		if strings.HasPrefix(text, `"`) {
			var s string
			if err := json.Unmarshal(value, &s); err == nil {
				text = s
			}
		}
		out[key] = text
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
