package openaichatcompletions

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/hangxie/chatops/planner"
	"github.com/hangxie/chatops/tool"
	"github.com/hangxie/chatops/tool/reply"
)

// stepsFromMessage maps one assistant message to plan steps: prose and reply
// calls become reply steps into conv, other tool calls become "<scheme>://"
// steps. Model output is untrusted, so each call is resolved via funcs and
// validated before a step is produced. It errors on bad JSON, an unavailable
// function, empty reply text, or invalid arguments.
func stepsFromMessage(msg respMessage, funcs map[string]toolFunc) (planner.Plan, error) {
	var steps []planner.Step
	if text := strings.TrimSpace(msg.Content); text != "" {
		steps = append(steps, replyStep(text, nil))
	}
	for _, call := range msg.ToolCalls {
		// A typed function may declare non-string scalars, so arguments
		// decode as raw JSON and are stringified later (see paramsToStrings).
		var args map[string]json.RawMessage
		if raw := call.Function.Arguments; raw != "" {
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				return planner.Plan{}, fmt.Errorf("openai: decode arguments for %q: %w", call.Function.Name, err)
			}
		}
		name := call.Function.Name
		if name == replyFunc {
			text := stringArg(args["text"])
			if strings.TrimSpace(text) == "" {
				return planner.Plan{}, fmt.Errorf("openai: reply call has empty text")
			}
			steps = append(steps, replyStep(text, nil))
			continue
		}
		tf, ok := funcs[name]
		if !ok {
			return planner.Plan{}, fmt.Errorf("openai: completion called unavailable function %q", name)
		}
		if err := validateArgs(name, tf.params, args); err != nil {
			return planner.Plan{}, err
		}
		steps = append(steps, planner.Step{
			Tool: tf.scheme + "://",
			Call: tool.Call{Arguments: paramsToStrings(tf.params, args)},
		})
	}
	return planner.Plan{Steps: steps}, nil
}

// validateArgs rejects a tool call whose arguments violate the tool's flat
// schema: a missing-required or undeclared parameter, or a wrong scalar
// type. JSON null and empty values count as absent, matching
// paramsToStrings.
func validateArgs(fn string, params []tool.Param, args map[string]json.RawMessage) error {
	declared := make(map[string]tool.Param, len(params))
	for _, p := range params {
		declared[p.Name] = p
	}
	for name := range args {
		if _, ok := declared[name]; !ok {
			return fmt.Errorf("openai: function %q got undeclared parameter %q", fn, name)
		}
	}
	for _, p := range params {
		raw, present := args[p.Name]
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

// stringArg decodes a raw JSON value as a string, returning "" when it is
// absent or not a JSON string.
func stringArg(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
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
		// JSON Schema defines "integer" mathematically, so an integer-valued
		// number in any JSON form ("3", "3.0", "1e3") is accepted; only a
		// non-integer value (e.g. "3.5") is rejected.
		if _, ok := canonicalInt(raw); !ok {
			return errors.New("must be an integer")
		}
	}
	return nil
}

// canonicalInt reports whether raw is a JSON number with an integer value and
// returns its canonical decimal form ("1e3" -> "1000", "3.0" -> "3"). It uses
// big.Rat so large integers keep full precision, unlike a float64 round-trip.
func canonicalInt(raw json.RawMessage) (string, bool) {
	r := new(big.Rat)
	if _, ok := r.SetString(strings.TrimSpace(string(raw))); !ok || !r.IsInt() {
		return "", false
	}
	return r.Num().String(), true
}

// paramsToStrings converts raw JSON parameter values into the string map
// tool.Call carries: a JSON string yields its value, an integer parameter its
// canonical decimal form (so a tool that parses it as an int is not tripped by
// "3.0" or "1e3"), and other scalars their literal text. Null/empty are
// dropped, and it returns nil when none remain.
func paramsToStrings(params []tool.Param, raw map[string]json.RawMessage) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	types := make(map[string]string, len(params))
	for _, p := range params {
		types[p.Name] = p.Type
	}
	out := make(map[string]string, len(raw))
	for key, value := range raw {
		text := strings.TrimSpace(string(value))
		if text == "" || text == "null" {
			continue
		}
		switch {
		case types[key] == "integer":
			if canon, ok := canonicalInt(value); ok {
				text = canon
			}
		case strings.HasPrefix(text, `"`):
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

// replyStep is a step posting text back to the requester through the reply
// tool, mirroring the shape the ping planner emits. The target conversation
// is injected by the executor, so the step carries only the text and any
// interactive choices.
func replyStep(text string, choices []tool.Choice) planner.Step {
	return planner.Step{Tool: reply.URL, Call: tool.Call{
		Arguments: map[string]string{"text": text},
		Choices:   choices,
	}}
}
