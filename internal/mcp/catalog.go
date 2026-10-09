package mcp

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tool is one MCP tool under its model-facing name; server-supplied fields are untrusted and imply no safety.
type Tool struct {
	Name        string // model-facing name, see modelName
	Server      string // configured server ID
	MCPName     string // tool name on the MCP server
	Description string
	InputSchema json.RawMessage

	Hints Hints

	schema *jsonschema.Resolved
}

// Hints are the server's untrusted MCP annotations with spec defaults; policy may only tighten with them.
type Hints struct {
	ReadOnly    bool // readOnlyHint, default false
	Destructive bool // destructiveHint, default true
	Idempotent  bool // idempotentHint, default false
	OpenWorld   bool // openWorldHint, default true
}

func newHints(annotations *mcpsdk.ToolAnnotations) Hints {
	if annotations == nil {
		return Hints{Destructive: true, OpenWorld: true}
	}
	return Hints{
		ReadOnly:    annotations.ReadOnlyHint,
		Destructive: annotations.DestructiveHint == nil || *annotations.DestructiveHint,
		Idempotent:  annotations.IdempotentHint,
		OpenWorld:   annotations.OpenWorldHint == nil || *annotations.OpenWorldHint,
	}
}

// clone copies the mutable parts so snapshot data cannot be changed by callers.
func (t Tool) clone() Tool {
	t.InputSchema = bytes.Clone(t.InputSchema)
	return t
}

// Validate checks args against the input schema; empty or null args mean {}.
func (t Tool) Validate(args json.RawMessage) error {
	_, err := t.arguments(args)
	return err
}

// arguments validates args and returns that same object verbatim, so the server gets exactly what was checked.
func (t Tool) arguments(args json.RawMessage) (map[string]json.RawMessage, error) {
	if t.schema == nil {
		return nil, fmt.Errorf("tool %s has no resolved input schema; use a tool from a Catalog", t.Name)
	}
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage("null")
	}
	var value any
	if err := json.Unmarshal(args, &value); err != nil {
		return nil, fmt.Errorf("arguments are not valid JSON: %w", err)
	}
	if value == nil {
		value = map[string]any{}
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, errors.New("arguments must be a JSON object")
	}
	if err := t.schema.Validate(value); err != nil {
		return nil, err
	}

	// Cannot fail: args already decoded above as a JSON object or null.
	var exact map[string]json.RawMessage
	_ = json.Unmarshal(args, &exact)
	if exact == nil {
		exact = map[string]json.RawMessage{}
	}
	return exact, nil
}

// Catalog is an immutable snapshot of all servers' tools; accessors return copies.
type Catalog struct {
	tools  []Tool
	byName map[string]int
}

// Tools returns the tools sorted by model-facing name.
func (c *Catalog) Tools() []Tool {
	if c == nil {
		return nil
	}
	tools := make([]Tool, len(c.tools))
	for i, tool := range c.tools {
		tools[i] = tool.clone()
	}
	return tools
}

// Lookup finds a tool by model-facing name.
func (c *Catalog) Lookup(name string) (Tool, bool) {
	if c == nil {
		return Tool{}, false
	}
	i, ok := c.byName[name]
	if !ok {
		return Tool{}, false
	}
	return c.tools[i].clone(), true
}

// NewCatalog builds a catalog from tools listed per server ID, skipping and reporting unusable ones.
func NewCatalog(listed map[string][]*mcpsdk.Tool) (*Catalog, []error) {
	servers := make([]string, 0, len(listed))
	for server := range listed {
		servers = append(servers, server)
	}
	slices.Sort(servers)

	var tools []Tool
	var skipped []error
	owner := map[string]string{}
	for _, server := range servers {
		for _, listedTool := range listed[server] {
			identity := server + "/" + listedTool.Name
			tool, err := newTool(server, listedTool)
			if err != nil {
				skipped = append(skipped, fmt.Errorf("%s: %w", identity, err))
				continue
			}
			if previous, ok := owner[tool.Name]; ok {
				skipped = append(skipped, fmt.Errorf("%s: model name %q already used by %s", identity, tool.Name, previous))
				continue
			}
			owner[tool.Name] = identity
			tools = append(tools, tool)
		}
	}

	slices.SortFunc(tools, func(a, b Tool) int { return cmp.Compare(a.Name, b.Name) })
	byName := make(map[string]int, len(tools))
	for i, tool := range tools {
		byName[tool.Name] = i
	}
	return &Catalog{tools: tools, byName: byName}, skipped
}

func newTool(server string, listed *mcpsdk.Tool) (Tool, error) {
	raw, err := json.Marshal(listed.InputSchema)
	if err != nil {
		return Tool{}, fmt.Errorf("input schema: %w", err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return Tool{}, fmt.Errorf("input schema: %w", err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return Tool{}, fmt.Errorf("input schema: %w", err)
	}

	return Tool{
		Name:        modelName(server, listed.Name),
		Server:      server,
		MCPName:     listed.Name,
		Description: listed.Description,
		InputSchema: raw,
		Hints:       newHints(listed.Annotations),
		schema:      resolved,
	}, nil
}
