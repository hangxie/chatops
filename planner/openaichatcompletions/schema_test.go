package openaichatcompletions

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// downgraded runs the downgrade pass and decodes the result for comparison.
func downgraded(t *testing.T, raw any) map[string]any {
	t.Helper()
	out, err := downgradeSchema(raw)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(out, &decoded))
	return decoded
}

func Test_downgradeSchema_keeps_usable_keywords(t *testing.T) {
	testCases := map[string]struct {
		in   any
		want map[string]any
	}{
		"nil-becomes-empty-object": {
			in:   nil,
			want: map[string]any{"type": "object", "properties": map[string]any{}},
		},
		"bare-object": {
			in:   map[string]any{"type": "object"},
			want: map[string]any{"type": "object"},
		},
		"properties-and-required": {
			in: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"service": map[string]any{"type": "string", "description": "the service"},
				},
				"required": []any{"service"},
			},
			want: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"service": map[string]any{"type": "string", "description": "the service"},
				},
				"required": []any{"service"},
			},
		},
		"enum-preserved": {
			in: map[string]any{"type": "object", "properties": map[string]any{
				"output": map[string]any{"type": "string", "enum": []any{"brief", "json"}},
			}},
			want: map[string]any{"type": "object", "properties": map[string]any{
				"output": map[string]any{"type": "string", "enum": []any{"brief", "json"}},
			}},
		},
		"array-items": {
			in: map[string]any{"type": "object", "properties": map[string]any{
				"names": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			}},
			want: map[string]any{"type": "object", "properties": map[string]any{
				"names": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			}},
		},
		"missing-type-defaults-to-object": {
			in:   map[string]any{"properties": map[string]any{"a": map[string]any{"type": "string"}}},
			want: map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, downgraded(t, tc.in))
		})
	}
}

func Test_downgradeSchema_drops_unsupported_keywords(t *testing.T) {
	testCases := map[string]struct {
		in      any
		dropped []string
	}{
		"additionalProperties": {
			in: map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{"a": map[string]any{"type": "string"}},
			},
			dropped: []string{"additionalProperties"},
		},
		"schema-and-id": {
			in: map[string]any{
				"type": "object", "$schema": "https://json-schema.org/draft/2020-12/schema",
				"$id": "urn:x", "properties": map[string]any{},
			},
			dropped: []string{"$schema", "$id"},
		},
		"nested-additionalProperties": {
			in: map[string]any{"type": "object", "properties": map[string]any{
				"inner": map[string]any{"type": "object", "additionalProperties": false},
			}},
			dropped: []string{"additionalProperties"},
		},
		"patternProperties-and-not": {
			in: map[string]any{
				"type": "object", "patternProperties": map[string]any{"^a": map[string]any{}},
				"not": map[string]any{"type": "string"}, "properties": map[string]any{},
			},
			dropped: []string{"patternProperties", "not"},
		},
		"allOf": {
			in:      map[string]any{"type": "object", "allOf": []any{map[string]any{"type": "object"}}, "properties": map[string]any{}},
			dropped: []string{"allOf"},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			out, err := downgradeSchema(tc.in)
			require.NoError(t, err)
			// Gemini's OpenAI-compatible endpoint rejects several of these
			// outright, so they must not survive at any depth.
			for _, keyword := range tc.dropped {
				require.NotContains(t, string(out), keyword)
			}
		})
	}
}

func Test_downgradeSchema_inlines_refs(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"target": map[string]any{"$ref": "#/$defs/Target"},
		},
		"$defs": map[string]any{
			"Target": map[string]any{"type": "string", "description": "where to act"},
		},
	}

	got := downgraded(t, schema)

	props := got["properties"].(map[string]any)
	require.Equal(t, map[string]any{"type": "string", "description": "where to act"}, props["target"])
	require.NotContains(t, got, "$defs")
}

func Test_downgradeSchema_inlines_legacy_definitions(t *testing.T) {
	schema := map[string]any{
		"type":        "object",
		"properties":  map[string]any{"target": map[string]any{"$ref": "#/definitions/Target"}},
		"definitions": map[string]any{"Target": map[string]any{"type": "integer"}},
	}

	props := downgraded(t, schema)["properties"].(map[string]any)
	require.Equal(t, map[string]any{"type": "integer"}, props["target"])
}

func Test_downgradeSchema_skips_malformed_subschemas(t *testing.T) {
	testCases := map[string]struct {
		in    any
		check func(t *testing.T, got map[string]any)
	}{
		"items-not-an-object": {
			in: map[string]any{"type": "object", "properties": map[string]any{
				"names": map[string]any{"type": "array", "items": "string"},
			}},
			check: func(t *testing.T, got map[string]any) {
				props := got["properties"].(map[string]any)
				require.Equal(t, map[string]any{"type": "array"}, props["names"])
			},
		},
		"property-not-an-object": {
			in: map[string]any{"type": "object", "properties": map[string]any{
				"a": "nope",
				"b": map[string]any{"type": "string"},
			}},
			check: func(t *testing.T, got map[string]any) {
				props := got["properties"].(map[string]any)
				require.NotContains(t, props, "a")
				require.Contains(t, props, "b")
			},
		},
		"properties-not-an-object": {
			in: map[string]any{"type": "object", "properties": "nope"},
			check: func(t *testing.T, got map[string]any) {
				require.NotContains(t, got, "properties")
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			// A malformed subschema costs only itself: the rest of the schema
			// is still offered, widened rather than rejected.
			tc.check(t, downgraded(t, tc.in))
		})
	}
}

func Test_downgradeSchema_rejects_unresolvable_refs(t *testing.T) {
	testCases := map[string]any{
		"external": map[string]any{"type": "object", "properties": map[string]any{
			"a": map[string]any{"$ref": "https://example.test/schema.json"},
		}},
		"missing-def": map[string]any{"type": "object", "properties": map[string]any{
			"a": map[string]any{"$ref": "#/$defs/Nope"},
		}},
		"no-defs-section": map[string]any{"type": "object", "properties": map[string]any{
			"a": map[string]any{"$ref": "#/$defs/Target"},
		}, "$defs": "not-an-object"},
		"target-not-an-object": map[string]any{"type": "object", "properties": map[string]any{
			"a": map[string]any{"$ref": "#/$defs/Target"},
		}, "$defs": map[string]any{"Target": "not-an-object"}},
		"short-ref": map[string]any{"type": "object", "properties": map[string]any{
			"a": map[string]any{"$ref": "#/$defs/"},
		}, "$defs": map[string]any{"Target": map[string]any{"type": "string"}}},
		"inside-array-items": map[string]any{"type": "object", "properties": map[string]any{
			"names": map[string]any{
				"type":  "array",
				"items": map[string]any{"$ref": "https://example.test/x"},
			},
		}},
	}

	for name, schema := range testCases {
		t.Run(name, func(t *testing.T) {
			_, err := downgradeSchema(schema)
			require.ErrorContains(t, err, "cannot resolve schema reference")
		})
	}
}

func Test_downgradeSchema_collapses_single_branch(t *testing.T) {
	testCases := map[string]struct {
		in   any
		want map[string]any
	}{
		"nullable-anyOf": {
			in: map[string]any{"type": "object", "properties": map[string]any{
				"name": map[string]any{"anyOf": []any{
					map[string]any{"type": "string"},
					map[string]any{"type": "null"},
				}},
			}},
			want: map[string]any{"type": "string"},
		},
		"nullable-oneOf": {
			in: map[string]any{"type": "object", "properties": map[string]any{
				"name": map[string]any{"oneOf": []any{
					map[string]any{"type": "null"},
					map[string]any{"type": "integer"},
				}},
			}},
			want: map[string]any{"type": "integer"},
		},
		"outer-description-wins": {
			in: map[string]any{"type": "object", "properties": map[string]any{
				"name": map[string]any{
					"description": "outer",
					"anyOf": []any{
						map[string]any{"type": "string", "description": "inner"},
						map[string]any{"type": "null"},
					},
				},
			}},
			want: map[string]any{"type": "string", "description": "outer"},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			props := downgraded(t, tc.in)["properties"].(map[string]any)
			require.Equal(t, tc.want, props["name"])
		})
	}
}

func Test_downgradeSchema_drops_ambiguous_branches(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"value": map[string]any{"oneOf": []any{
			map[string]any{"type": "string"},
			map[string]any{"type": "integer"},
		}},
	}}

	// Two real branches cannot be expressed in the subset, so the constraint
	// is dropped rather than misdescribed: a wider schema is safe because the
	// server validates the call.
	props := downgraded(t, schema)["properties"].(map[string]any)
	require.Equal(t, map[string]any{}, props["value"])
}

func Test_downgradeSchema_folds_const_into_enum(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"mode": map[string]any{"type": "string", "const": "brief"},
	}}

	props := downgraded(t, schema)["properties"].(map[string]any)
	require.Equal(t, map[string]any{"type": "string", "enum": []any{"brief"}}, props["mode"])
}

func Test_downgradeSchema_reduces_type_lists(t *testing.T) {
	testCases := map[string]struct {
		in   any
		want any
	}{
		"nullable-string": {in: []any{"string", "null"}, want: "string"},
		"null-first":      {in: []any{"null", "integer"}, want: "integer"},
		"only-null":       {in: []any{"null"}, want: nil},
		"not-a-type":      {in: 42, want: nil},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			schema := map[string]any{"type": "object", "properties": map[string]any{
				"a": map[string]any{"type": tc.in},
			}}
			props := downgraded(t, schema)["properties"].(map[string]any)
			field := props["a"].(map[string]any)
			if tc.want == nil {
				require.NotContains(t, field, "type")
				return
			}
			require.Equal(t, tc.want, field["type"])
		})
	}
}

func Test_downgradeSchema_rejects_non_objects(t *testing.T) {
	testCases := map[string]any{
		"string": "nope",
		"number": 42,
		"array":  []any{"a"},
	}

	for name, schema := range testCases {
		t.Run(name, func(t *testing.T) {
			_, err := downgradeSchema(schema)
			require.ErrorContains(t, err, "schema is not an object")
		})
	}
}

func Test_downgradeSchema_rejects_unencodable(t *testing.T) {
	_, err := downgradeSchema(map[string]any{"type": make(chan int)})
	require.ErrorContains(t, err, "encode schema")
}

func Test_downgradeSchema_widens_recursive_schemas(t *testing.T) {
	// A self-referential schema — a tree-shaped filter, say — must not cost
	// the whole tool. Past the depth bound the subschema is replaced by an
	// unconstrained one, which only widens what the model may send.
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"self": map[string]any{"$ref": "#/$defs/Node"}},
		"$defs": map[string]any{
			"Node": map[string]any{
				"type":       "object",
				"properties": map[string]any{"next": map[string]any{"$ref": "#/$defs/Node"}},
			},
		},
	}

	got := downgraded(t, schema)

	require.Equal(t, "object", got["type"])
	require.Contains(t, got["properties"].(map[string]any), "self")
	require.True(t, json.Valid(mustJSON(got)))
}

func Test_downgradeSchema_widens_long_reference_chains(t *testing.T) {
	defs := map[string]any{}
	for i := range 20 {
		defs[fmt.Sprintf("N%d", i)] = map[string]any{"$ref": fmt.Sprintf("#/$defs/N%d", i+1)}
	}
	defs["N20"] = map[string]any{"type": "string"}
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"a": map[string]any{"$ref": "#/$defs/N0"}},
		"$defs":      defs,
	}

	got := downgraded(t, schema)

	require.Contains(t, got["properties"].(map[string]any), "a")
	require.True(t, json.Valid(mustJSON(got)))
}

func Test_downgradeSchema_collapses_a_lone_allOf_ref(t *testing.T) {
	// Some generators wrap a single "$ref" in an "allOf"; without collapsing
	// it the property would lose its type entirely.
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"target": map[string]any{"allOf": []any{map[string]any{"$ref": "#/$defs/Target"}}},
		},
		"$defs": map[string]any{"Target": map[string]any{"type": "string", "description": "where"}},
	}

	props := downgraded(t, schema)["properties"].(map[string]any)
	require.Equal(t, map[string]any{"type": "string", "description": "where"}, props["target"])
}

func Test_downgradeSchema_prunes_dangling_required(t *testing.T) {
	// A property whose subschema could not be converted is left out, and
	// endpoints reject a "required" entry naming a property that is absent.
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"kept":    map[string]any{"type": "string"},
			"dropped": "not-a-schema",
		},
		"required": []any{"kept", "dropped"},
	}

	got := downgraded(t, schema)

	props := got["properties"].(map[string]any)
	require.Contains(t, props, "kept")
	require.NotContains(t, props, "dropped")
	require.Equal(t, []any{"kept"}, got["required"])
}

func Test_downgradeSchema_drops_required_entirely_when_nothing_remains(t *testing.T) {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"gone": "not-a-schema"},
		"required":   []any{"gone"},
	}

	require.NotContains(t, downgraded(t, schema), "required")
}

func Test_downgradeSchema_keeps_an_empty_properties_object(t *testing.T) {
	// A tool declaring an empty object schema and one declaring none should
	// be offered the same shape; some endpoints are particular about it.
	declared := downgraded(t, map[string]any{"type": "object", "properties": map[string]any{}})
	absent := downgraded(t, nil)

	require.Equal(t, absent, declared)
	require.Equal(t, map[string]any{}, declared["properties"])
}

func Test_downgradeSchema_reports_a_bad_collapsed_branch(t *testing.T) {
	// The single branch of a oneOf is downgraded too, so a broken reference
	// inside it fails the whole tool rather than being silently kept.
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"a": map[string]any{"anyOf": []any{
			map[string]any{"$ref": "https://example.test/x"},
			map[string]any{"type": "null"},
		}},
	}}

	_, err := downgradeSchema(schema)
	require.ErrorContains(t, err, "cannot resolve schema reference")
}

func Test_downgradeSchema_widens_deep_nesting(t *testing.T) {
	deepest := map[string]any{"type": "string"}
	for range 20 {
		deepest = map[string]any{"type": "object", "properties": map[string]any{"a": deepest}}
	}

	got := downgraded(t, deepest)

	// The outer levels survive; only what sits past the bound is unconstrained.
	require.Equal(t, "object", got["type"])
	require.True(t, json.Valid(mustJSON(got)))
}

func Test_downgradeSchema_accepts_raw_json(t *testing.T) {
	// A server may send its schema as raw JSON rather than a decoded map.
	raw := json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}},"additionalProperties":false}`)

	got := downgraded(t, raw)

	require.Equal(t, "object", got["type"])
	require.NotContains(t, got, "additionalProperties")
}

func Test_downgradeSchema_handles_real_inferred_schema(t *testing.T) {
	// The shape mcp.AddTool infers from a Go struct, including the
	// additionalProperties:false that Gemini rejects.
	raw := json.RawMessage(`{
		"type": "object",
		"additionalProperties": false,
		"required": ["kind"],
		"properties": {
			"kind": {"type": "string", "description": "Resource type."},
			"all-namespaces": {"type": "boolean", "description": "List across all namespaces."}
		}
	}`)

	got := downgraded(t, raw)

	require.NotContains(t, got, "additionalProperties")
	require.Equal(t, []any{"kind"}, got["required"])
	props := got["properties"].(map[string]any)
	require.Equal(t, "boolean", props["all-namespaces"].(map[string]any)["type"])
	require.NotEmpty(t, props["kind"].(map[string]any)["description"])

	// The downgraded schema must still be valid JSON for the request body.
	require.True(t, json.Valid(mustJSON(got)))
	require.False(t, strings.Contains(string(mustJSON(got)), "$"))
}
