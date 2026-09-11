package openaichatcompletions

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
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
			want: map[string]any{"type": "object", "properties": map[string]any{}},
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

func Test_downgradeSchema_resolves_any_local_pointer(t *testing.T) {
	// "$defs" and "definitions" are where a generator usually puts a target,
	// but any JSON Pointer into the document is as valid — and rejecting one
	// costs the whole tool, since a schema that cannot be inlined cannot be
	// offered.
	testCases := map[string]struct {
		schema map[string]any
		want   map[string]any
	}{
		"sibling property": {
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "the query"},
					"also":  map[string]any{"$ref": "#/properties/query"},
				},
			},
			want: map[string]any{"type": "string", "description": "the query"},
		},
		"nested path": {
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"also": map[string]any{"$ref": "#/$defs/Outer/properties/inner"},
				},
				"$defs": map[string]any{"Outer": map[string]any{
					"properties": map[string]any{"inner": map[string]any{"type": "integer"}},
				}},
			},
			want: map[string]any{"type": "integer"},
		},
		"array index": {
			schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"also": map[string]any{"$ref": "#/$defs/Choice/anyOf/0"}},
				"$defs": map[string]any{"Choice": map[string]any{
					"anyOf": []any{map[string]any{"type": "boolean"}, map[string]any{"type": "null"}},
				}},
			},
			want: map[string]any{"type": "boolean"},
		},
		"escaped tokens": {
			schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"also": map[string]any{"$ref": "#/$defs/a~1b~0c"}},
				"$defs":      map[string]any{"a/b~c": map[string]any{"type": "number"}},
			},
			want: map[string]any{"type": "number"},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			props := downgraded(t, tc.schema)["properties"].(map[string]any)
			require.Equal(t, tc.want, props["also"])
		})
	}
}

func Test_downgradeSchema_resolves_the_document_root(t *testing.T) {
	// "#" names the document itself, which is how a recursive schema refers
	// back to its own top level.
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"child": map[string]any{"$ref": "#"}},
	}

	// It resolves rather than failing; the recursion is stopped by the depth
	// bound, which widens.
	got := downgraded(t, schema)
	require.Equal(t, "object", got["type"])
	require.Contains(t, got["properties"].(map[string]any), "child")
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
				// Dropped and replaced by an empty one, as for an object
				// that declares no properties at all.
				require.Equal(t, map[string]any{}, got["properties"])
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
		"index past the end": map[string]any{
			"type":       "object",
			"properties": map[string]any{"a": map[string]any{"$ref": "#/$defs/Choice/anyOf/9"}},
			"$defs":      map[string]any{"Choice": map[string]any{"anyOf": []any{map[string]any{"type": "boolean"}}}},
		},
		"negative index": map[string]any{
			"type":       "object",
			"properties": map[string]any{"a": map[string]any{"$ref": "#/$defs/Choice/anyOf/-1"}},
			"$defs":      map[string]any{"Choice": map[string]any{"anyOf": []any{map[string]any{"type": "boolean"}}}},
		},
		"non-numeric index": map[string]any{
			"type":       "object",
			"properties": map[string]any{"a": map[string]any{"$ref": "#/$defs/Choice/anyOf/first"}},
			"$defs":      map[string]any{"Choice": map[string]any{"anyOf": []any{map[string]any{"type": "boolean"}}}},
		},
		"path through a scalar": map[string]any{
			"type":       "object",
			"properties": map[string]any{"a": map[string]any{"$ref": "#/$defs/Name/nope"}},
			"$defs":      map[string]any{"Name": "not-a-schema"},
		},
		"points at a scalar": map[string]any{
			"type":       "object",
			"properties": map[string]any{"a": map[string]any{"$ref": "#/$defs/Name"}},
			"$defs":      map[string]any{"Name": "not-a-schema"},
		},
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
		// Two real types are a genuine alternative; picking one would say the
		// tool rejects the other, so the keyword goes instead.
		"two real types":   {in: []any{"string", "integer"}, want: nil},
		"three real types": {in: []any{"string", "integer", "null"}, want: nil},
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

func Test_downgradeSchema_always_gives_an_object_properties(t *testing.T) {
	// The shape schema inference actually emits for a tool that reads
	// nothing: no properties key at all, and an additionalProperties that
	// gets dropped. Without settling it, ping and status-list would be
	// offered a bare {"type":"object"}.
	inferred, err := jsonschema.For[struct{}](nil)
	require.NoError(t, err)
	encoded, err := json.Marshal(inferred)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "properties\":{")

	var wire map[string]any
	require.NoError(t, json.Unmarshal(encoded, &wire))

	testCases := map[string]any{
		"inferred-from-an-empty-struct": wire,
		"declared-empty":                map[string]any{"type": "object", "properties": map[string]any{}},
		"absent-entirely":               nil,
		"bare-object":                   map[string]any{"type": "object"},
	}

	want := map[string]any{"type": "object", "properties": map[string]any{}}
	for name, schema := range testCases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, want, downgraded(t, schema))
		})
	}
}

func Test_downgradeSchema_leaves_a_free_form_root_alone(t *testing.T) {
	// A tool whose input is free-form says so with additionalProperties, and
	// that keyword is dropped on the way out. Declaring an empty properties
	// in its place would say the tool accepts no fields at all.
	testCases := map[string]any{
		"open":            true,
		"typed-values":    map[string]any{"type": "string"},
		"closed-is-empty": false,
	}

	for name, additional := range testCases {
		t.Run(name, func(t *testing.T) {
			got := downgraded(t, map[string]any{"type": "object", "additionalProperties": additional})

			require.NotContains(t, got, "additionalProperties")
			if name == "closed-is-empty" {
				// additionalProperties:false is what inference emits for a
				// tool that reads nothing, which is the case the empty
				// properties exists for.
				require.Equal(t, map[string]any{}, got["properties"])
				return
			}
			require.NotContains(t, got, "properties")
		})
	}
}

func Test_downgradeSchema_does_not_narrow_past_the_depth_bound(t *testing.T) {
	// Conversion replaces what sits past the bound with an unconstrained
	// schema; adding an empty properties map would narrow it straight back.
	deep := map[string]any{"type": "object", "additionalProperties": true}
	for range 20 {
		deep = map[string]any{"anyOf": []any{deep, map[string]any{"type": "null"}}}
	}

	got := downgraded(t, deep)

	require.Equal(t, "object", got["type"])
	require.NotContains(t, got, "properties")
}

func Test_downgradeSchema_sees_through_a_wrapped_root(t *testing.T) {
	// A root that is nothing but a "$ref", or a lone branch, describes the
	// tool through its target. Reading additionalProperties off the wrapper
	// would find nothing and call a free-form tool closed.
	freeFormArgs := map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}
	closedArgs := map[string]any{"type": "object", "additionalProperties": false}

	testCases := map[string]struct {
		in         any
		wantClosed bool
	}{
		"ref to free-form": {
			in: map[string]any{"$ref": "#/$defs/Args", "$defs": map[string]any{"Args": freeFormArgs}},
		},
		"ref to closed": {
			in:         map[string]any{"$ref": "#/$defs/Args", "$defs": map[string]any{"Args": closedArgs}},
			wantClosed: true,
		},
		"allOf around a ref to free-form": {
			in: map[string]any{
				"allOf": []any{map[string]any{"$ref": "#/$defs/Args"}},
				"$defs": map[string]any{"Args": freeFormArgs},
			},
		},
		"anyOf around free-form": {
			in: map[string]any{"anyOf": []any{freeFormArgs, map[string]any{"type": "null"}}},
		},
		"chained refs to free-form": {
			in: map[string]any{
				"$ref": "#/$defs/Outer",
				"$defs": map[string]any{
					"Outer": map[string]any{"$ref": "#/$defs/Args"},
					"Args":  freeFormArgs,
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got := downgraded(t, tc.in)

			require.Equal(t, "object", got["type"])
			if tc.wantClosed {
				require.Equal(t, map[string]any{}, got["properties"])
				return
			}
			require.NotContains(t, got, "properties", "a free-form tool was narrowed to accepting no fields")
		})
	}
}

// Test_downgradeSchema_documents_what_is_stripped records the treatment of
// every construct the downgrade knows about.
//
// Dropping an unrecognised keyword is the point — it is what keeps a schema
// inside the subset the completion endpoints accept — but it also means a
// keyword MCP gains later vanishes silently. This table is where that shows
// up: a new construct has no entry, and adding one is a decision about
// whether it survives.
func Test_downgradeSchema_documents_what_is_stripped(t *testing.T) {
	// The kept set itself is pinned, so widening it is deliberate.
	kept := make([]string, 0, len(keptKeywords))
	for keyword := range keptKeywords {
		kept = append(kept, keyword)
	}
	sort.Strings(kept)
	require.Equal(t, []string{"description", "enum", "items", "properties", "required", "type"}, kept)

	field := func(schema map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": map[string]any{"a": schema}}
	}

	testCases := map[string]struct {
		what string
		in   map[string]any
		want map[string]any
	}{
		"type":        {what: "kept", in: map[string]any{"type": "string"}, want: map[string]any{"type": "string"}},
		"description": {what: "kept", in: map[string]any{"type": "string", "description": "d"}, want: map[string]any{"type": "string", "description": "d"}},
		"enum":        {what: "kept", in: map[string]any{"type": "string", "enum": []any{"a"}}, want: map[string]any{"type": "string", "enum": []any{"a"}}},
		"items":       {what: "kept", in: map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, want: map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},

		"const":              {what: "folded into enum", in: map[string]any{"type": "string", "const": "x"}, want: map[string]any{"type": "string", "enum": []any{"x"}}},
		"nullable type list": {what: "reduced to one type", in: map[string]any{"type": []any{"string", "null"}}, want: map[string]any{"type": "string"}},
		"sole anyOf branch": {
			what: "collapsed",
			in:   map[string]any{"anyOf": []any{map[string]any{"type": "integer"}, map[string]any{"type": "null"}}},
			want: map[string]any{"type": "integer"},
		},

		"additionalProperties": {what: "dropped", in: map[string]any{"type": "object", "additionalProperties": false}, want: map[string]any{"type": "object"}},
		"$schema":              {what: "dropped", in: map[string]any{"type": "string", "$schema": "https://json-schema.org/draft/2020-12/schema"}, want: map[string]any{"type": "string"}},
		"$id":                  {what: "dropped", in: map[string]any{"type": "string", "$id": "urn:x"}, want: map[string]any{"type": "string"}},
		"not":                  {what: "dropped", in: map[string]any{"type": "string", "not": map[string]any{"const": "x"}}, want: map[string]any{"type": "string"}},
		"patternProperties":    {what: "dropped", in: map[string]any{"type": "object", "patternProperties": map[string]any{"^a": map[string]any{}}}, want: map[string]any{"type": "object"}},
		"format":               {what: "dropped", in: map[string]any{"type": "string", "format": "date-time"}, want: map[string]any{"type": "string"}},
		"default":              {what: "dropped", in: map[string]any{"type": "string", "default": "x"}, want: map[string]any{"type": "string"}},
		"minimum":              {what: "dropped", in: map[string]any{"type": "integer", "minimum": 1}, want: map[string]any{"type": "integer"}},
		"pattern":              {what: "dropped", in: map[string]any{"type": "string", "pattern": "^a"}, want: map[string]any{"type": "string"}},
		"ambiguous oneOf": {
			what: "dropped entirely; two real branches cannot be expressed",
			in:   map[string]any{"oneOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "integer"}}},
			want: map[string]any{},
		},
	}

	for name, tc := range testCases {
		t.Run(name+" is "+tc.what, func(t *testing.T) {
			got := downgraded(t, field(tc.in))
			require.Equal(t, tc.want, got["properties"].(map[string]any)["a"])
		})
	}
}

func Test_freeForm(t *testing.T) {
	// Nested wrappers, deeper than the bound allows following.
	deep := map[string]any{"type": "object", "additionalProperties": true}
	for range 20 {
		deep = map[string]any{"anyOf": []any{deep, map[string]any{"type": "null"}}}
	}

	testCases := map[string]struct {
		node map[string]any
		root map[string]any
		want bool
	}{
		"nil node":            {node: nil, root: map[string]any{}},
		"plain object":        {node: map[string]any{"type": "object"}, root: map[string]any{}},
		"closed object":       {node: map[string]any{"additionalProperties": false}, root: map[string]any{}},
		"open object":         {node: map[string]any{"additionalProperties": true}, root: map[string]any{}, want: true},
		"typed values":        {node: map[string]any{"additionalProperties": map[string]any{"type": "string"}}, root: map[string]any{}, want: true},
		"outer wins over ref": {node: map[string]any{"additionalProperties": true, "$ref": "#/$defs/Nope"}, root: map[string]any{}, want: true},
		// An unresolvable reference says nothing either way; the closed
		// answer is the one that changes least, and downgrading has already
		// rejected such a schema before this is reached.
		"unresolvable ref": {node: map[string]any{"$ref": "https://example.test/x"}, root: map[string]any{}},
		// Past the bound the chain is no longer followed, and the answer is
		// the widening one: conversion has already replaced what sits there
		// with an unconstrained schema.
		"deeper than the bound": {node: deep, root: map[string]any{}, want: true},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, freeForm(tc.node, tc.root, 0))
		})
	}
}

func Test_downgradeSchema_leaves_a_non_object_root_alone(t *testing.T) {
	// MCP input schemas are objects, but nothing stops a server sending
	// something else; it must not be handed a properties map that would make
	// no sense for it.
	got := downgraded(t, map[string]any{"type": "array", "items": map[string]any{"type": "string"}})

	require.Equal(t, "array", got["type"])
	require.NotContains(t, got, "properties")
}

func Test_downgradeSchema_leaves_nested_objects_alone(t *testing.T) {
	// Only the tool's own schema is settled. A map-typed field carries its
	// value schema in additionalProperties, which is dropped, so giving what
	// remains an empty properties would say "an object with no fields" where
	// the tool meant "any object" — narrowing rather than widening.
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"labels": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
	}}

	labels := downgraded(t, schema)["properties"].(map[string]any)["labels"].(map[string]any)

	// Just the type: additionalProperties is gone, and nothing was invented
	// in its place.
	require.Equal(t, map[string]any{"type": "object"}, labels)
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
