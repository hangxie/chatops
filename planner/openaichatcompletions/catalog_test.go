package openaichatcompletions

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/tool"
)

// funcSchema is the decoded shape of one action function's JSON Schema.
type funcSchema struct {
	Type       string `json:"type"`
	Properties struct {
		Target *struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"target"`
		Parameters *struct {
			Properties map[string]struct {
				Type        string `json:"type"`
				Description string `json:"description"`
			} `json:"properties"`
			Required []string `json:"required"`
		} `json:"parameters"`
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

func Test_buildCatalog_offers_reply_and_per_action_functions(t *testing.T) {
	descriptors := map[string]tool.Descriptor{
		"status": {Summary: "status summary", Actions: []tool.Action{{Name: "check"}}},
		"ping":   {Summary: "ping summary", Actions: []tool.Action{{Name: "ping"}}},
	}
	defs, funcs, err := buildCatalog([]string{"status", "ping"}, descriptors)
	require.NoError(t, err)

	names := make([]string, len(defs))
	for i, def := range defs {
		require.Equal(t, "function", def.Type)
		names[i] = def.Function.Name
	}
	// reply is always first; then one function per action, schemes sorted.
	require.Equal(t, []string{"reply", "ping-ping", "status-check"}, names)

	// The reply function's schema requires text; each tool function names a
	// concrete action and maps back to its (scheme, action).
	require.JSONEq(t, string(replyParams), string(defs[0].Function.Parameters))
	require.Equal(t, "ping", funcs["ping-ping"].scheme)
	require.Equal(t, "ping", funcs["ping-ping"].action.Name)
	require.Equal(t, "status", funcs["status-check"].scheme)
	require.Equal(t, "check", funcs["status-check"].action.Name)
	require.Len(t, funcs, 2)

	byName := defsByName(defs)
	require.Equal(t, "ping summary", byName["ping-ping"].Description)
}

func Test_validateSchemes(t *testing.T) {
	testCases := map[string]struct {
		schemes []string
		wantErr string
	}{
		"valid":              {schemes: []string{"ping", "status", "k8s-prod"}},
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
	schemes := []string{"status", "ping"}
	descriptors := map[string]tool.Descriptor{
		"status": {Summary: "s", Actions: []tool.Action{{Name: "check"}}},
		"ping":   {Summary: "p", Actions: []tool.Action{{Name: "ping"}}},
	}
	_, _, err := buildCatalog(schemes, descriptors)
	require.NoError(t, err)
	require.Equal(t, []string{"status", "ping"}, schemes)
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
		"invalid-action-char": {
			schemes:     []string{"status"},
			descriptors: map[string]tool.Descriptor{"status": {Summary: "s", Actions: []tool.Action{{Name: "check it"}}}},
			wantErr:     "invalid function name",
		},
		"too-long-combined": {
			schemes:     []string{"status"},
			descriptors: map[string]tool.Descriptor{"status": {Summary: "s", Actions: []tool.Action{{Name: strings.Repeat("a", 64)}}}},
			wantErr:     "invalid function name",
		},
		"name-collision": {
			// "a-b" + "c" and "a" + "b-c" both yield "a-b-c".
			schemes: []string{"a-b", "a"},
			descriptors: map[string]tool.Descriptor{
				"a-b": {Summary: "s", Actions: []tool.Action{{Name: "c"}}},
				"a":   {Summary: "s", Actions: []tool.Action{{Name: "b-c"}}},
			},
			wantErr: "collides",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, _, err := buildCatalog(tc.schemes, tc.descriptors)
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func Test_buildCatalog_builds_action_functions(t *testing.T) {
	descriptors := map[string]tool.Descriptor{
		"status": {
			Summary: "check external service status",
			Actions: []tool.Action{
				{Name: "check", Description: "check one", TakesTarget: true, TargetDesc: "the service"},
				{Name: "list", Description: "list all"},
			},
		},
	}
	defs, funcs, err := buildCatalog([]string{"status"}, descriptors)
	require.NoError(t, err)

	byName := defsByName(defs)
	require.Contains(t, byName, "status-check")
	require.Contains(t, byName, "status-list")

	// check: description combines summary and action; target is present and
	// required; no parameters are declared.
	check := byName["status-check"]
	require.Equal(t, "check external service status — check one", check.Description)
	cs := decodeSchema(t, check.Parameters)
	require.Equal(t, "object", cs.Type)
	require.NotNil(t, cs.Properties.Target)
	require.Equal(t, "the service", cs.Properties.Target.Description)
	require.Equal(t, []string{"target"}, cs.Required)
	require.Nil(t, cs.Properties.Parameters)
	require.Equal(t, "status", funcs["status-check"].scheme)
	require.Equal(t, "check", funcs["status-check"].action.Name)

	// list: takes no target, so nothing is required.
	list := byName["status-list"]
	require.Equal(t, "check external service status — list all", list.Description)
	ls := decodeSchema(t, list.Parameters)
	require.Nil(t, ls.Properties.Target)
	require.Empty(t, ls.Required)
	require.Equal(t, "status", funcs["status-list"].scheme)
	require.Equal(t, "list", funcs["status-list"].action.Name)
}

func Test_actionSchema_target_and_required_params(t *testing.T) {
	// An action with a target and a required parameter: both "target" and
	// "parameters" are required, and the required param is listed.
	scale := tool.Action{
		Name: "scale", TakesTarget: true, TargetDesc: "the deployment",
		Parameters: []tool.Param{
			{Name: "replicas", Type: "integer", Required: true, Description: "desired count"},
			{Name: "force", Type: "boolean"},
		},
	}
	s := decodeSchema(t, mustJSON(actionSchema(scale)))
	require.Equal(t, []string{"target", "parameters"}, s.Required)
	require.Equal(t, "integer", s.Properties.Parameters.Properties["replicas"].Type)
	require.Equal(t, "boolean", s.Properties.Parameters.Properties["force"].Type)
	require.Equal(t, []string{"replicas"}, s.Properties.Parameters.Required)

	// An action whose only parameter is optional keeps parameters present
	// but unrequired; the target is still required.
	restart := tool.Action{
		Name: "restart", TakesTarget: true, TargetDesc: "the deployment",
		Parameters: []tool.Param{{Name: "replicas", Type: "integer"}},
	}
	r := decodeSchema(t, mustJSON(actionSchema(restart)))
	require.Equal(t, []string{"target"}, r.Required)
	require.NotNil(t, r.Properties.Parameters)
	require.Empty(t, r.Properties.Parameters.Required)
}

func Test_actionSchema_no_target_and_default_type(t *testing.T) {
	// No target; a required param makes "parameters" required, and a param
	// with no type defaults to string. The schema uses no const or oneOf.
	set := tool.Action{
		Name: "set",
		Parameters: []tool.Param{
			{Name: "key", Required: true}, // no Type -> string
			{Name: "value", Type: "string"},
		},
	}
	raw := mustJSON(actionSchema(set))
	require.NotContains(t, string(raw), "oneOf")
	require.NotContains(t, string(raw), "const")

	s := decodeSchema(t, raw)
	require.Nil(t, s.Properties.Target)
	require.Equal(t, []string{"parameters"}, s.Required)
	require.Equal(t, "string", s.Properties.Parameters.Properties["key"].Type)
	require.Equal(t, []string{"key"}, s.Properties.Parameters.Required)
}

func Test_actionSchema_no_target_no_params(t *testing.T) {
	// An action with neither target nor parameters is an empty object with
	// no required list.
	raw := mustJSON(actionSchema(tool.Action{Name: "ping"}))
	s := decodeSchema(t, raw)
	require.Equal(t, "object", s.Type)
	require.Nil(t, s.Properties.Target)
	require.Nil(t, s.Properties.Parameters)
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

func Test_actionFuncDesc(t *testing.T) {
	testCases := map[string]struct {
		summary string
		action  tool.Action
		want    string
	}{
		"summary-and-action": {summary: "the tool", action: tool.Action{Description: "does x"}, want: "the tool — does x"},
		"action-only":        {summary: "", action: tool.Action{Description: "does x"}, want: "does x"},
		"summary-only":       {summary: "the tool", action: tool.Action{}, want: "the tool"},
		"neither":            {summary: "", action: tool.Action{}, want: ""},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, actionFuncDesc(tc.summary, tc.action))
		})
	}
}
