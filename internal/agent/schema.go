package agent

import (
	"encoding/json"
	"slices"
)

// modelSchema drops "null" from type lists such as ["null","array"], which some models answer with a
// JSON-encoded string instead of the value. Unparseable schemas pass through unchanged.
func modelSchema(raw json.RawMessage) json.RawMessage {
	var schema any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return raw
	}
	dropNullTypes(schema)
	// Cannot fail: the value was just decoded from JSON.
	simplified, _ := json.Marshal(schema)
	return simplified
}

// Keywords whose value is a subschema or a list of them, and keywords whose value maps names to subschemas.
// Only these are walked, so literal data under default, const, or examples is left as is.
var (
	subschemaKeywords = []string{
		"items", "prefixItems", "additionalItems", "additionalProperties", "unevaluatedItems", "unevaluatedProperties",
		"propertyNames", "contains", "not", "if", "then", "else", "anyOf", "oneOf", "allOf",
	}
	subschemaMapKeywords = []string{"properties", "patternProperties", "dependentSchemas", "$defs", "definitions"}
)

func dropNullTypes(node any) {
	schema, ok := node.(map[string]any)
	if !ok {
		return
	}
	if types, ok := schema["type"].([]any); ok {
		schema["type"] = nonNullTypes(types)
	}
	for _, keyword := range subschemaKeywords {
		switch child := schema[keyword].(type) {
		case []any:
			for _, sub := range child {
				dropNullTypes(sub)
			}
		default:
			dropNullTypes(child)
		}
	}
	for _, keyword := range subschemaMapKeywords {
		if named, ok := schema[keyword].(map[string]any); ok {
			for _, sub := range named {
				dropNullTypes(sub)
			}
		}
	}
}

// nonNullTypes keeps a lone remaining type as a string, and the list as is when only "null" is listed.
func nonNullTypes(types []any) any {
	kept := slices.DeleteFunc(slices.Clone(types), func(t any) bool { return t == "null" })
	switch len(kept) {
	case 0:
		return types
	case 1:
		return kept[0]
	}
	return kept
}
