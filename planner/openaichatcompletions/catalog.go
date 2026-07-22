package openaichatcompletions

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"

	"github.com/hangxie/chatops/tool"
)

// replyFunc is the built-in function the model calls to reply to the
// requester; it is always offered, alongside the operational tools.
const replyFunc = "reply"

// replyFuncDesc is the description the model sees for the reply function.
const replyFuncDesc = "Post a message back to the person who sent the request, " +
	"for answers, clarifying questions, or acknowledgements."

// replyParams is the JSON Schema for the built-in reply function: it takes
// only the message text. Operational tools get a typed schema built at
// runtime from their descriptor (see actionSchema).
var replyParams = mustJSON(map[string]any{
	"type": "object",
	"properties": map[string]any{
		"text": map[string]any{
			"type":        "string",
			"description": "The message text to post back to the requester.",
		},
	},
	"required": []string{"text"},
})

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

// toolFunc is the (scheme, action) one offered function invokes, kept so a
// tool call's arguments can be validated against the action's schema.
type toolFunc struct {
	scheme string
	action tool.Action
}

// functionName joins scheme and action with a hyphen (e.g. "status-check"),
// so the name carries the action and the schema needs no discriminator.
func functionName(scheme, action string) string {
	return scheme + "-" + action
}

// buildCatalog returns the functions offered to the model — reply plus one
// typed function per (scheme, action) — and the reverse map from function
// name to the action it invokes. An invalid, too-long, or colliding
// generated name is a configuration error.
func buildCatalog(schemes []string, descriptors map[string]tool.Descriptor) ([]toolDef, map[string]toolFunc, error) {
	capacity := 1 // reply, plus one function per action below
	for _, scheme := range schemes {
		capacity += len(descriptors[scheme].Actions)
	}
	defs := make([]toolDef, 0, capacity)
	defs = append(defs, toolDef{Type: "function", Function: functionDef{
		Name:        replyFunc,
		Description: replyFuncDesc,
		Parameters:  replyParams,
	}})
	funcs := map[string]toolFunc{}

	sorted := append([]string(nil), schemes...)
	sort.Strings(sorted)
	for _, scheme := range sorted {
		d, ok := descriptors[scheme]
		if !ok {
			return nil, nil, fmt.Errorf("openai: no descriptor for enabled tool %q", scheme)
		}
		for _, a := range d.Actions {
			name := functionName(scheme, a.Name)
			if len(name) > maxFuncNameLen || !funcNameRE.MatchString(name) {
				return nil, nil, fmt.Errorf("openai: tool %q action %q yields invalid function name %q (allowed: letters, digits, '_', '-'; max %d characters)", scheme, a.Name, name, maxFuncNameLen)
			}
			if _, dup := funcs[name]; dup {
				return nil, nil, fmt.Errorf("openai: tool %q action %q yields function name %q that collides with another offered function", scheme, a.Name, name)
			}
			funcs[name] = toolFunc{scheme: scheme, action: a}
			defs = append(defs, toolDef{Type: "function", Function: functionDef{
				Name:        name,
				Description: actionFuncDesc(d.Summary, a),
				Parameters:  mustJSON(actionSchema(a)),
			}})
		}
	}
	return defs, funcs, nil
}

// actionFuncDesc joins the tool summary and the action description so the
// model sees both the tool's purpose and what this action does.
func actionFuncDesc(summary string, a tool.Action) string {
	switch {
	case summary != "" && a.Description != "":
		return summary + " — " + a.Description
	case a.Description != "":
		return a.Description
	default:
		return summary
	}
}

// actionSchema builds one action's function schema: a plain object with a
// "target" (required when the action takes one) and a "parameters" object
// (required when any parameter is). It avoids const/oneOf/additionalProperties
// to stay within the schema subset endpoints such as Gemini accept.
func actionSchema(a tool.Action) map[string]any {
	properties := map[string]any{}
	var required []string

	if a.TakesTarget {
		target := map[string]any{"type": "string"}
		if a.TargetDesc != "" {
			target["description"] = a.TargetDesc
		}
		properties["target"] = target
		required = append(required, "target")
	}

	if paramProps, paramRequired := actionParams(a.Parameters); len(paramProps) > 0 {
		paramSchema := map[string]any{
			"type":       "object",
			"properties": paramProps,
		}
		if len(paramRequired) > 0 {
			paramSchema["required"] = paramRequired
			required = append(required, "parameters")
		}
		properties["parameters"] = paramSchema
	}

	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// actionParams builds one action's parameter property schemas keyed by
// name, plus its sorted required-name list. A parameter with no declared
// type defaults to "string".
func actionParams(params []tool.Param) (map[string]any, []string) {
	props := map[string]any{}
	var required []string
	for _, p := range params {
		typ := p.Type
		if typ == "" {
			typ = "string"
		}
		schema := map[string]any{"type": typ}
		if p.Description != "" {
			schema["description"] = p.Description
		}
		props[p.Name] = schema
		if p.Required {
			required = append(required, p.Name)
		}
	}
	sort.Strings(required)
	return props, required
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
