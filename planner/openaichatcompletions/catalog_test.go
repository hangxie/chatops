package openaichatcompletions

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// funcSchema is the decoded shape of one offered function's JSON Schema.
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

// objectSchema builds a tool input schema with the given properties.
func objectSchema(required []string, props map[string]any) map[string]any {
	schema := map[string]any{"type": "object", "properties": props}
	if required != nil {
		schema["required"] = required
	}
	return schema
}

// discardLogger drops catalog warnings.
func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func Test_buildCatalog_offers_each_tool(t *testing.T) {
	tools := []*mcp.Tool{
		{Name: "reply", Description: "post a message", InputSchema: objectSchema([]string{"text"}, map[string]any{
			"text": map[string]any{"type": "string", "description": "the text"},
		})},
		{Name: "status-check", Description: "status check summary", InputSchema: objectSchema([]string{"service"}, map[string]any{
			"service": map[string]any{"type": "string", "description": "service to check"},
		})},
		{Name: "ping", Description: "liveness check", InputSchema: objectSchema(nil, map[string]any{})},
	}

	built := buildCatalog(tools, discardLogger())

	require.Len(t, built.defs, 3)
	require.Equal(t, map[string]bool{"reply": true, "status-check": true, "ping": true}, built.offered)

	byName := defsByName(built.defs)
	require.Equal(t, "status check summary", byName["status-check"].Description)

	// Each tool carries its own schema, so the model is told the arguments
	// the tool actually reads rather than guessing them.
	check := decodeSchema(t, byName["status-check"].Parameters)
	require.Equal(t, "object", check.Type)
	require.Equal(t, "string", check.Properties["service"].Type)
	require.Equal(t, "service to check", check.Properties["service"].Description)
	require.Equal(t, []string{"service"}, check.Required)

	// A tool that reads nothing still declares an object schema.
	ping := decodeSchema(t, byName["ping"].Parameters)
	require.Equal(t, "object", ping.Type)
	require.Empty(t, ping.Required)
}

func Test_buildCatalog_preserves_order(t *testing.T) {
	tools := []*mcp.Tool{
		{Name: "zulu", InputSchema: objectSchema(nil, nil)},
		{Name: "alpha", InputSchema: objectSchema(nil, nil)},
	}

	built := buildCatalog(tools, discardLogger())

	// The catalog order is the host's, which already sorts within each
	// server; the planner does not reorder it.
	require.Equal(t, "zulu", built.defs[0].Function.Name)
	require.Equal(t, "alpha", built.defs[1].Function.Name)
}

func Test_buildCatalog_skips_unusable_tools(t *testing.T) {
	testCases := map[string]struct {
		tool   *mcp.Tool
		reason string
	}{
		"no-name":     {tool: &mcp.Tool{Name: ""}, reason: "has no name"},
		"bad-charset": {tool: &mcp.Tool{Name: "bad name"}, reason: "not a valid function name"},
		"too-long":    {tool: &mcp.Tool{Name: strings.Repeat("a", 65)}, reason: "exceeds 64 characters"},
		"bad-schema":  {tool: &mcp.Tool{Name: "broken", InputSchema: map[string]any{"$ref": "https://example.test/x"}}, reason: "cannot resolve schema reference"},
		"non-object":  {tool: &mcp.Tool{Name: "scalar", InputSchema: "nope"}, reason: "schema is not an object"},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))

			// One unusable tool must cost only that tool: the rest of the
			// catalog is still offered, so a bad external server cannot make
			// every request fail.
			built := buildCatalog([]*mcp.Tool{tc.tool, {Name: "ping", InputSchema: objectSchema(nil, nil)}}, logger)

			require.Equal(t, map[string]bool{"ping": true}, built.offered)
			require.Len(t, built.defs, 1)
			require.Contains(t, logs.String(), "tool not offered to the model")
			require.Contains(t, logs.String(), tc.reason)
		})
	}
}

func Test_buildCatalog_skips_duplicates_and_nils(t *testing.T) {
	var logs bytes.Buffer
	tools := []*mcp.Tool{
		nil,
		{Name: "ping", Description: "first", InputSchema: objectSchema(nil, nil)},
		{Name: "ping", Description: "second", InputSchema: objectSchema(nil, nil)},
	}

	built := buildCatalog(tools, slog.New(slog.NewTextHandler(&logs, nil)))

	require.Len(t, built.defs, 1)
	require.Equal(t, "first", built.defs[0].Function.Description)
	require.Contains(t, logs.String(), "duplicate function name")
}

func Test_buildCatalog_empty(t *testing.T) {
	built := buildCatalog(nil, discardLogger())

	require.Empty(t, built.defs)
	require.Empty(t, built.offered)
}

func Test_checkFuncName(t *testing.T) {
	testCases := map[string]struct {
		name   string
		errMsg string
	}{
		"simple":     {name: "ping"},
		"dashes":     {name: "status-check"},
		"underscore": {name: "search_repos"},
		"digits":     {name: "k8s-get"},
		"max-length": {name: strings.Repeat("a", 64)},
		"empty":      {name: "", errMsg: "has no name"},
		"too-long":   {name: strings.Repeat("a", 65), errMsg: "exceeds 64"},
		"dot":        {name: "a.b", errMsg: "not a valid function name"},
		"slash":      {name: "a/b", errMsg: "not a valid function name"},
		"space":      {name: "a b", errMsg: "not a valid function name"},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			err := checkFuncName(tc.name)
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
		})
	}
}

func Test_mustJSON_panics_on_unmarshalable(t *testing.T) {
	require.Panics(t, func() { mustJSON(make(chan int)) })
}
