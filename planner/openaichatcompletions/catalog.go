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
// runtime from their descriptor (see toolSchema).
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

// toolFunc is the tool one offered function invokes, kept so a call's
// arguments can be validated against the tool's parameters.
type toolFunc struct {
	scheme string
	params []tool.Param
}

// buildCatalog returns the functions offered to the model — reply plus one
// typed function per tool scheme — and the reverse map from function name to
// the tool it invokes. Each tool performs a single intent, so its scheme is
// the function name and its schema is a flat object of the tool's arguments.
// An invalid or too-long scheme is a configuration error.
func buildCatalog(schemes []string, descriptors map[string]tool.Descriptor) ([]toolDef, map[string]toolFunc, error) {
	defs := make([]toolDef, 0, len(schemes)+1)
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
		if len(scheme) > maxFuncNameLen || !funcNameRE.MatchString(scheme) {
			return nil, nil, fmt.Errorf("openai: tool %q yields invalid function name (allowed: letters, digits, '_', '-'; max %d characters)", scheme, maxFuncNameLen)
		}
		if _, dup := funcs[scheme]; dup {
			return nil, nil, fmt.Errorf("openai: tool %q collides with another offered function", scheme)
		}
		funcs[scheme] = toolFunc{scheme: scheme, params: d.Parameters}
		defs = append(defs, toolDef{Type: "function", Function: functionDef{
			Name:        scheme,
			Description: d.Description,
			Parameters:  mustJSON(toolSchema(d.Parameters)),
		}})
	}
	return defs, funcs, nil
}

// toolSchema builds one tool's function schema: a flat object whose
// properties are the tool's arguments, with the required ones listed. It
// avoids const/oneOf/additionalProperties to stay within the schema subset
// endpoints such as Gemini accept.
func toolSchema(params []tool.Param) map[string]any {
	props, required := paramSchemas(params)
	schema := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// paramSchemas builds a tool's parameter property schemas keyed by name,
// plus its sorted required-name list. A parameter with no declared type
// defaults to "string".
func paramSchemas(params []tool.Param) (map[string]any, []string) {
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
