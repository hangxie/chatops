package mcp

import (
	"encoding/json"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

var objectSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"name": map[string]any{"type": "string"},
	},
	"required":             []any{"name"},
	"additionalProperties": false,
}

func Test_buildCatalog(t *testing.T) {
	tru, fls := true, false
	listed := map[string][]*mcpsdk.Tool{
		"zeta": {{Name: "ping", InputSchema: map[string]any{"type": "object"}}},
		"alpha": {
			{Name: "get", Description: "Get a thing.", InputSchema: objectSchema, Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: true}},
			{Name: "delete", InputSchema: map[string]any{"type": "object"}, Annotations: &mcpsdk.ToolAnnotations{DestructiveHint: &fls}},
			{Name: "wipe", InputSchema: map[string]any{"type": "object"}, Annotations: &mcpsdk.ToolAnnotations{DestructiveHint: &tru, IdempotentHint: true, OpenWorldHint: &fls}},
			{Name: "bad-ref", InputSchema: map[string]any{"$ref": "https://example.com/schema.json"}},
			{Name: "bad-json", InputSchema: func() {}},
			{Name: "bad-type", InputSchema: map[string]any{"type": 5}},
		},
	}

	catalog, skipped := buildCatalog(listed)

	names := []string{}
	for _, tool := range catalog.Tools() {
		names = append(names, tool.Name)
	}
	require.Equal(t, []string{"alpha__delete", "alpha__get", "alpha__wipe", "zeta__ping"}, names)
	require.Len(t, skipped, 3)
	require.Contains(t, skipped[0].Error(), `alpha/bad-ref: input schema`)
	require.Contains(t, skipped[1].Error(), `alpha/bad-json: input schema`)
	require.Contains(t, skipped[2].Error(), `alpha/bad-type: input schema`)

	get, ok := catalog.Lookup("alpha__get")
	require.True(t, ok)
	require.Equal(t, "alpha", get.Server)
	require.Equal(t, "get", get.MCPName)
	require.Equal(t, "Get a thing.", get.Description)
	// Unset hints take MCP defaults: destructive and open-world.
	require.Equal(t, Hints{ReadOnly: true, Destructive: true, OpenWorld: true}, get.Hints)
	require.JSONEq(t, `{"type":"object","properties":{"name":{"type":"string"}},"required":["name"],"additionalProperties":false}`, string(get.InputSchema))

	del, _ := catalog.Lookup("alpha__delete")
	require.Equal(t, Hints{Destructive: false, OpenWorld: true}, del.Hints)

	wipe, _ := catalog.Lookup("alpha__wipe")
	require.Equal(t, Hints{Destructive: true, Idempotent: true}, wipe.Hints)

	ping, _ := catalog.Lookup("zeta__ping")
	require.Equal(t, Hints{Destructive: true, OpenWorld: true}, ping.Hints)

	_, ok = catalog.Lookup("alpha__bad-ref")
	require.False(t, ok)
}

func Test_buildCatalog_name_collision(t *testing.T) {
	// Hash suffixes prevent natural collisions, so list the same tool twice.
	listed := map[string][]*mcpsdk.Tool{
		"s": {
			{Name: "dup", InputSchema: map[string]any{"type": "object"}},
			{Name: "dup", InputSchema: map[string]any{"type": "object"}},
		},
	}
	catalog, skipped := buildCatalog(listed)
	require.Len(t, catalog.Tools(), 1)
	require.Len(t, skipped, 1)
	require.Contains(t, skipped[0].Error(), `s/dup: model name "s__dup" already used by s/dup`)
}

func Test_Tool_Validate(t *testing.T) {
	catalog, skipped := buildCatalog(map[string][]*mcpsdk.Tool{
		"s": {
			{Name: "get", InputSchema: objectSchema},
			{Name: "any", InputSchema: map[string]any{"type": "object"}},
		},
	})
	require.Empty(t, skipped)
	get, _ := catalog.Lookup("s__get")
	anyTool, _ := catalog.Lookup("s__any")

	tests := map[string]struct {
		tool   Tool
		args   string
		errMsg string
	}{
		"valid":           {tool: get, args: `{"name":"web"}`},
		"missing":         {tool: get, args: `{}`, errMsg: "name"},
		"wrong-type":      {tool: get, args: `{"name":1}`, errMsg: "name"},
		"extra":           {tool: get, args: `{"name":"x","ns":"y"}`, errMsg: "ns"},
		"not-json":        {tool: get, args: `{"name":`, errMsg: "arguments are not valid JSON"},
		"not-object":      {tool: anyTool, args: `[1]`, errMsg: "arguments must be a JSON object"},
		"empty-is-object": {tool: anyTool, args: ``},
		"null-is-object":  {tool: anyTool, args: `null`},
		"no-schema":       {tool: Tool{Name: "x__y", Server: "x", MCPName: "y"}, args: `{}`, errMsg: "tool x__y has no resolved input schema; use a tool from a Catalog"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := tc.tool.Validate(json.RawMessage(tc.args))
			if tc.errMsg == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.errMsg)
		})
	}
}

func Test_Catalog_returns_copies(t *testing.T) {
	catalog, skipped := buildCatalog(map[string][]*mcpsdk.Tool{"one": {{Name: "get", InputSchema: objectSchema}}})
	require.Empty(t, skipped)
	original := string(catalog.Tools()[0].InputSchema)

	listed := catalog.Tools()
	listed[0].Name = "changed"
	listed[0].InputSchema[0] = 'X'
	looked, ok := catalog.Lookup("one__get")
	require.True(t, ok)
	looked.InputSchema[0] = 'Y'

	again, ok := catalog.Lookup("one__get")
	require.True(t, ok)
	require.Equal(t, "one__get", again.Name)
	require.Equal(t, original, string(again.InputSchema))
	require.Equal(t, original, string(catalog.Tools()[0].InputSchema))
}

func Test_Catalog_empty(t *testing.T) {
	var catalog *Catalog
	require.Empty(t, catalog.Tools())
	_, ok := catalog.Lookup("x")
	require.False(t, ok)
}
