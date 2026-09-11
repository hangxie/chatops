package reply

import "github.com/google/jsonschema-go/jsonschema"

// MustSchemaForTest exposes mustSchema so its programmer-error panic can be
// exercised; inference never fails for this package's own Args type.
func MustSchemaForTest(schema *jsonschema.Schema, err error) *jsonschema.Schema {
	return mustSchema(schema, err)
}

// MustResolveForTest exposes mustResolve for the same reason: resolving a
// schema this package inferred never fails.
func MustResolveForTest(resolved *jsonschema.Resolved, err error) *jsonschema.Resolved {
	return mustResolve(resolved, err)
}
