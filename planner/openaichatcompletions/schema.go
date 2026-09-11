package openaichatcompletions

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// MCP places no restriction on a tool's input schema beyond it being JSON
// Schema, but the function-calling APIs this planner talks to accept only a
// subset — and they disagree about which. Gemini's OpenAI-compatible endpoint
// rejects "additionalProperties"; several endpoints reject "$ref", "oneOf",
// "allOf", and "not". A schema is therefore downgraded to the common subset
// before it is offered, rather than passed through as the server wrote it.
//
// Downgrading is lossy on purpose: a dropped keyword only widens what the
// model may send, and the server validates the call anyway. What cannot be
// expressed at all — an unresolvable $ref — fails, and the caller drops that
// one tool instead of failing every request.

// maxSchemaDepth bounds recursion, so a self-referential or absurdly nested
// schema from an external server cannot exhaust the stack.
const maxSchemaDepth = 12

// keptKeywords are the schema keywords carried through the downgrade. Every
// other keyword is dropped: either the endpoints reject it, or it only
// constrains values the server will validate itself.
//
// Dropping is silent by design — an unknown keyword is exactly what should
// not reach an endpoint that may reject the whole request over it — which
// means a keyword MCP gains later disappears without anyone noticing. The
// treatment of every construct this file knows about is therefore pinned in
// Test_downgradeSchema_documents_what_is_stripped, and this set is pinned
// with it, so growing either is a decision rather than an accident.
var keptKeywords = map[string]bool{
	"type":        true,
	"description": true,
	"enum":        true,
	"properties":  true,
	"required":    true,
	"items":       true,
}

// downgradeSchema converts a tool's input schema into the subset the
// completion endpoints accept, returning it ready to embed in a request.
//
// A tool that declares no schema gets the empty object schema, which is what
// "this tool takes no arguments" means to the API.
func downgradeSchema(raw any) (json.RawMessage, error) {
	if raw == nil {
		return mustJSON(emptyObjectSchema()), nil
	}
	// The schema arrives as whatever the client decoded; re-encoding it
	// normalizes json.RawMessage, typed structs, and map[string]any alike.
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encode schema: %w", err)
	}
	// json.Marshal produced these bytes, so they always decode.
	var decoded any
	_ = json.Unmarshal(encoded, &decoded)
	root, ok := decoded.(map[string]any)
	if !ok {
		return nil, errors.New("schema is not an object")
	}
	converted, err := convertSchema(root, root, 0)
	if err != nil {
		return nil, err
	}
	if _, hasType := converted["type"]; !hasType {
		converted["type"] = "object"
	}
	// The root's type may have just been defaulted, so its properties are
	// settled after it rather than inside convertSchema. The source is
	// consulted too, because what decides this — additionalProperties — has
	// already been dropped from the converted schema by now.
	ensureObjectProperties(converted, root)
	return mustJSON(converted), nil
}

// convertSchema downgrades one schema node. root is the document the node
// came from, so "$ref" can be resolved against its "$defs".
func convertSchema(node, root map[string]any, depth int) (map[string]any, error) {
	// Past the bound the subschema is replaced by an unconstrained one rather
	// than failing. A recursive $ref — a tree-shaped filter, say — would
	// otherwise cost the whole tool, and dropping a constraint only widens
	// what the model may send, which the server validates anyway.
	if depth > maxSchemaDepth {
		return map[string]any{}, nil
	}
	resolved, err := resolveRef(node, root, depth)
	if err != nil {
		return nil, err
	}
	node = resolved

	out := map[string]any{}
	for key, value := range node {
		if !keptKeywords[key] {
			continue
		}
		switch key {
		case "properties":
			props, err := convertProperties(value, root, depth)
			if err != nil {
				return nil, err
			}
			if props != nil {
				out[key] = props
			}
		case "items":
			items, ok := value.(map[string]any)
			if !ok {
				continue
			}
			converted, err := convertSchema(items, root, depth+1)
			if err != nil {
				return nil, err
			}
			out[key] = converted
		case "type":
			if typ, ok := singleType(value); ok {
				out[key] = typ
			}
		default:
			out[key] = value
		}
	}
	// A single-valued "const" is an enum of one; endpoints that reject
	// "const" accept that spelling and it carries the same meaning.
	if value, ok := node["const"]; ok {
		if _, taken := out["enum"]; !taken {
			out["enum"] = []any{value}
		}
	}
	if branch, ok := soleBranch(node); ok {
		merged, err := mergeBranch(out, branch, root, depth)
		if err != nil {
			return nil, err
		}
		out = merged
	}
	pruneRequired(out)
	return out, nil
}

// ensureObjectProperties gives an object schema a properties map when it has
// none.
//
// It is applied to a tool's own schema and nowhere else. A tool that reads
// nothing is the common case: schema inference emits
// {"type":"object","additionalProperties":false} with no properties at all,
// and dropping additionalProperties would otherwise leave a bare
// {"type":"object"}, which some endpoints are particular about.
//
// Nested nodes are deliberately left alone. A map-typed field carries its
// value schema in additionalProperties, which is dropped, so adding an empty
// properties to what remains would say "an object with no fields" where the
// tool meant "any object" — narrowing the schema rather than widening it,
// which is the one thing downgrading must not do.
func ensureObjectProperties(schema, source map[string]any) {
	if schema["type"] != "object" {
		return
	}
	if _, declared := schema["properties"]; declared {
		return
	}
	// A tool whose input is free-form says so with an additionalProperties
	// that is not false, and that keyword has been dropped by now. Declaring
	// an empty properties in its place would say the tool accepts no fields
	// at all, which is the same narrowing this avoids for nested nodes.
	if freeForm(source, source, 0) {
		return
	}
	schema["properties"] = map[string]any{}
}

// freeForm reports whether a schema accepts fields it does not name.
//
// It follows the indirection a schema may wrap its real definition in — a
// "$ref" into the document's own definitions, or a lone branch — because the
// answer has to come from the node that describes the tool rather than from
// the wrapper around it. A schema whose root is nothing but a "$ref" would
// otherwise look closed however free-form its target was.
//
// Any node along the way declaring a non-false additionalProperties settles
// it, which is the widening answer and so the safe one. Running out of depth
// gives the same answer for the same reason: conversion has already replaced
// whatever sits past the bound with an unconstrained schema, and declaring
// that it accepts no fields would narrow the very thing that was widened.
func freeForm(node, root map[string]any, depth int) bool {
	if node == nil {
		return false
	}
	if depth > maxSchemaDepth {
		return true
	}
	if additional, present := node["additionalProperties"]; present && additional != false {
		return true
	}
	if _, wraps := node["$ref"]; wraps {
		resolved, err := resolveRef(node, root, depth)
		if err != nil {
			return false
		}
		return freeForm(resolved, root, depth+1)
	}
	if branch, ok := soleBranch(node); ok {
		return freeForm(branch, root, depth+1)
	}
	return false
}

// pruneRequired drops required names with no matching property.
//
// A property whose subschema could not be converted is left out, and a
// "required" entry pointing at a name that is not in "properties" is rejected
// by some endpoints — so the constraint goes when the thing it constrains
// does.
func pruneRequired(schema map[string]any) {
	required, ok := schema["required"].([]any)
	if !ok {
		return
	}
	props, _ := schema["properties"].(map[string]any)
	kept := make([]any, 0, len(required))
	for _, name := range required {
		if key, ok := name.(string); ok {
			if _, declared := props[key]; declared {
				kept = append(kept, name)
			}
		}
	}
	if len(kept) == 0 {
		delete(schema, "required")
		return
	}
	schema["required"] = kept
}

// convertProperties downgrades each declared property.
func convertProperties(value any, root map[string]any, depth int) (map[string]any, error) {
	properties, ok := value.(map[string]any)
	if !ok {
		return nil, nil
	}
	out := make(map[string]any, len(properties))
	for name, raw := range properties {
		schema, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		converted, err := convertSchema(schema, root, depth+1)
		if err != nil {
			return nil, err
		}
		out[name] = converted
	}
	return out, nil
}

// resolveRef follows a local "$ref" to the schema it names.
//
// Any JSON Pointer into the document resolves, not just the "$defs" and
// "definitions" sections a generator most often writes: "#/properties/query"
// and deeper paths are as valid, and rejecting them costs the whole tool,
// since a schema that cannot be inlined cannot be offered. An external
// reference still cannot be inlined and is reported.
func resolveRef(node, root map[string]any, depth int) (map[string]any, error) {
	ref, ok := node["$ref"].(string)
	if !ok {
		return node, nil
	}
	if depth > maxSchemaDepth {
		// As in convertSchema: an unresolvably deep chain widens rather than
		// costing the tool.
		return map[string]any{}, nil
	}
	target, ok := resolvePointer(ref, root)
	if !ok {
		return nil, fmt.Errorf("cannot resolve schema reference %q", ref)
	}
	return resolveRef(target, root, depth+1)
}

// resolvePointer walks a local reference to the schema object it names.
//
// The reference must be a fragment ("#" for the document itself, "#/a/b" for
// a path within it); anything else names another document and cannot be
// inlined. Path segments are unescaped per RFC 6901, and a numeric segment
// indexes an array, so a branch such as "#/anyOf/0" resolves too.
func resolvePointer(ref string, root map[string]any) (map[string]any, bool) {
	if ref == "#" {
		return root, true
	}
	path, found := strings.CutPrefix(ref, "#/")
	if !found {
		return nil, false
	}
	var current any = root
	for _, segment := range strings.Split(path, "/") {
		token := unescapePointer(segment)
		switch container := current.(type) {
		case map[string]any:
			next, present := container[token]
			if !present {
				return nil, false
			}
			current = next
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(container) {
				return nil, false
			}
			current = container[index]
		default:
			return nil, false
		}
	}
	schema, ok := current.(map[string]any)
	return schema, ok
}

// unescapePointer decodes the two escapes RFC 6901 defines, in the order it
// requires: "~1" is a "/" within a segment, and "~0" a literal "~".
func unescapePointer(segment string) string {
	return strings.ReplaceAll(strings.ReplaceAll(segment, "~1", "/"), "~0", "~")
}

// soleBranch returns the single meaningful branch of a "oneOf", "anyOf", or
// "allOf", which is how a nullable or optional value is commonly spelled and
// how some generators wrap a lone "$ref". Two or more real branches cannot be
// downgraded without changing meaning, so they are dropped entirely —
// widening the schema rather than misdescribing it.
func soleBranch(node map[string]any) (map[string]any, bool) {
	for _, keyword := range []string{"oneOf", "anyOf", "allOf"} {
		branches, ok := node[keyword].([]any)
		if !ok {
			continue
		}
		var real []map[string]any
		for _, branch := range branches {
			schema, ok := branch.(map[string]any)
			if !ok || schema["type"] == "null" {
				continue
			}
			real = append(real, schema)
		}
		if len(real) == 1 {
			return real[0], true
		}
	}
	return nil, false
}

// mergeBranch folds a collapsed oneOf/anyOf branch into the node that carried
// it, letting the outer node's own keywords (a description, typically) win.
func mergeBranch(out, branch, root map[string]any, depth int) (map[string]any, error) {
	converted, err := convertSchema(branch, root, depth+1)
	if err != nil {
		return nil, err
	}
	for key, value := range converted {
		if _, taken := out[key]; !taken {
			out[key] = value
		}
	}
	return out, nil
}

// singleType reduces a "type" keyword to one name, which is what the
// endpoints accept.
//
// JSON Schema allows a list, and the common case is a nullable value
// (["string","null"]), where the non-null entry carries the whole meaning. A
// list with two real types is a genuine alternative, and picking one would
// say the tool rejects the other — so the keyword is dropped instead, leaving
// the type unconstrained. Narrowing is the one thing the downgrade must never
// do, even when the wider answer says less.
func singleType(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case []any:
		var real []string
		for _, item := range typed {
			if name, ok := item.(string); ok && name != "null" {
				real = append(real, name)
			}
		}
		if len(real) == 1 {
			return real[0], true
		}
	}
	return "", false
}

// emptyObjectSchema is the schema for a tool that takes no arguments.
func emptyObjectSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
