package openaichatcompletions

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/tool"
)

// funcSchema is the decoded shape of one tool function's flat JSON Schema.
type funcSchema struct {
	Type       string `json:"type"`
	Properties map[string]struct {
		Type        string `json:"type"`
		Description string `json:"description"`
	} `json:"properties"`
	Required []string `json:"required"`
}

func decodeSchema(t *testing.T, raw json.RawMessage) funcSchema {
	t.Helper()
	var s funcSchema
	require.NoError(t, json.Unmarshal(raw, &s))
	return s
}

func defsByName(defs []toolDef) map[string]functionDef {
	byName := map[string]functionDef{}
	for _, d := range defs {
		byName[d.Function.Name] = d.Function
	}
	return byName
}

func Test_buildCatalog_offers_reply_and_per_tool_functions(t *testing.T) {
	descriptors := map[string]tool.Descriptor{
		"status-check": {Description: "status check summary"},
		"ping":         {Description: "ping summary"},
	}
	defs, funcs, err := buildCatalog([]string{"status-check", "ping"}, descriptors)
	require.NoError(t, err)

	names := make([]string, len(defs))
	for i, def := range defs {
		require.Equal(t, "function", def.Type)
		names[i] = def.Function.Name
	}
	// reply is always first; then one function per tool, schemes sorted.
	require.Equal(t, []string{"reply", "ping", "status-check"}, names)

	// The reply function's schema requires text; each tool function is named
	// by its scheme and maps back to it.
	require.JSONEq(t, string(replyParams), string(defs[0].Function.Parameters))
	require.Equal(t, "ping", funcs["ping"].scheme)
	require.Equal(t, "status-check", funcs["status-check"].scheme)
	require.Len(t, funcs, 2)

	byName := defsByName(defs)
	require.Equal(t, "ping summary", byName["ping"].Description)
	require.Equal(t, "status check summary", byName["status-check"].Description)
}

func Test_validateSchemes(t *testing.T) {
	testCases := map[string]struct {
		schemes []string
		wantErr string
	}{
		"valid":              {schemes: []string{"ping", "status-check", "k8s-prod"}},
		"empty":              {schemes: nil},
		"dot-invalid":        {schemes: []string{"service.status"}, wantErr: "cannot be an OpenAI function name"},
		"plus-invalid":       {schemes: []string{"a+b"}, wantErr: "cannot be an OpenAI function name"},
		"too-long":           {schemes: []string{strings.Repeat("a", 65)}, wantErr: "cannot be an OpenAI function name"},
		"max-length-ok":      {schemes: []string{strings.Repeat("a", 64)}},
		"reply-collision":    {schemes: []string{"reply"}, wantErr: "collides with the built-in reply"},
		"one-bad-among-good": {schemes: []string{"ping", "bad.name"}, wantErr: "bad.name"},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			err := validateSchemes(tc.schemes)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func Test_buildCatalog_with_no_schemes_still_offers_reply(t *testing.T) {
	defs, funcs, err := buildCatalog(nil, nil)
	require.NoError(t, err)
	require.Len(t, defs, 1)
	require.Equal(t, replyFunc, defs[0].Function.Name)
	require.Empty(t, funcs)
}

func Test_buildCatalog_does_not_mutate_input(t *testing.T) {
	schemes := []string{"status-check", "ping"}
	descriptors := map[string]tool.Descriptor{
		"status-check": {Description: "s"},
		"ping":         {Description: "p"},
	}
	_, _, err := buildCatalog(schemes, descriptors)
	require.NoError(t, err)
	require.Equal(t, []string{"status-check", "ping"}, schemes)
}

func Test_buildCatalog_errors_on_missing_descriptor(t *testing.T) {
	// Every enabled tool must describe itself; a scheme without a
	// descriptor is a wiring bug reported as an error.
	_, _, err := buildCatalog([]string{"ping"}, nil)
	require.ErrorContains(t, err, `no descriptor for enabled tool "ping"`)
}

func Test_buildCatalog_rejects_invalid_and_colliding_names(t *testing.T) {
	testCases := map[string]struct {
		schemes     []string
		descriptors map[string]tool.Descriptor
		wantErr     string
	}{
		"invalid-scheme-char": {
			schemes:     []string{"check it"},
			descriptors: map[string]tool.Descriptor{"check it": {Description: "s"}},
			wantErr:     "invalid function name",
		},
		"too-long-scheme": {
			schemes:     []string{strings.Repeat("a", 65)},
			descriptors: map[string]tool.Descriptor{strings.Repeat("a", 65): {Description: "s"}},
			wantErr:     "invalid function name",
		},
		"duplicate-scheme": {
			schemes:     []string{"dup", "dup"},
			descriptors: map[string]tool.Descriptor{"dup": {Description: "s"}},
			wantErr:     "collides",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, _, err := buildCatalog(tc.schemes, tc.descriptors)
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func Test_buildCatalog_builds_tool_functions(t *testing.T) {
	descriptors := map[string]tool.Descriptor{
		"status-check": {
			Description: "check one external service",
			Parameters: []tool.Param{
				{Name: "service", Type: "string", Required: true, Description: "the service"},
			},
		},
		"status-list": {Description: "list all services"},
	}
	defs, funcs, err := buildCatalog([]string{"status-check", "status-list"}, descriptors)
	require.NoError(t, err)

	byName := defsByName(defs)
	require.Contains(t, byName, "status-check")
	require.Contains(t, byName, "status-list")

	// check: description is the tool's; the required "service" argument sits
	// directly on the flat schema.
	check := byName["status-check"]
	require.Equal(t, "check one external service", check.Description)
	cs := decodeSchema(t, check.Parameters)
	require.Equal(t, "object", cs.Type)
	require.Equal(t, "the service", cs.Properties["service"].Description)
	require.Equal(t, "string", cs.Properties["service"].Type)
	require.Equal(t, []string{"service"}, cs.Required)
	require.Equal(t, "status-check", funcs["status-check"].scheme)

	// list: takes no arguments, so nothing is required.
	list := byName["status-list"]
	require.Equal(t, "list all services", list.Description)
	ls := decodeSchema(t, list.Parameters)
	require.Empty(t, ls.Properties)
	require.Empty(t, ls.Required)
	require.Equal(t, "status-list", funcs["status-list"].scheme)
}

func Test_toolSchema_required_and_optional_params(t *testing.T) {
	// A tool with a required and an optional parameter lists only the
	// required one, and preserves each declared scalar type.
	params := []tool.Param{
		{Name: "replicas", Type: "integer", Required: true, Description: "desired count"},
		{Name: "force", Type: "boolean"},
	}
	s := decodeSchema(t, mustJSON(toolSchema(params)))
	require.Equal(t, "object", s.Type)
	require.Equal(t, "integer", s.Properties["replicas"].Type)
	require.Equal(t, "boolean", s.Properties["force"].Type)
	require.Equal(t, []string{"replicas"}, s.Required)
}

func Test_toolSchema_default_type_and_no_const_or_oneOf(t *testing.T) {
	// A parameter with no declared type defaults to string; the schema uses
	// no const or oneOf so restrictive endpoints accept it.
	params := []tool.Param{
		{Name: "key", Required: true}, // no Type -> string
		{Name: "value", Type: "string"},
	}
	raw := mustJSON(toolSchema(params))
	require.NotContains(t, string(raw), "oneOf")
	require.NotContains(t, string(raw), "const")

	s := decodeSchema(t, raw)
	require.Equal(t, "string", s.Properties["key"].Type)
	require.Equal(t, []string{"key"}, s.Required)
}

func Test_toolSchema_no_params(t *testing.T) {
	// A tool with no parameters is an empty object with no required list.
	s := decodeSchema(t, mustJSON(toolSchema(nil)))
	require.Equal(t, "object", s.Type)
	require.Empty(t, s.Properties)
	require.Empty(t, s.Required)
}

func Test_replyParams_is_valid_json(t *testing.T) {
	var v any
	require.NoError(t, json.Unmarshal(replyParams, &v))
}

func Test_mustJSON_panics_on_unmarshalable_value(t *testing.T) {
	// A channel cannot be marshaled, exercising the panic path used to
	// catch a malformed static schema at startup.
	require.PanicsWithValue(t, "openai: marshal static schema: json: unsupported type: chan int", func() {
		mustJSON(make(chan int))
	})
}
