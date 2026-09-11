package openaichatcompletions

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxFuncNameLen is the OpenAI limit on function names.
const maxFuncNameLen = 64

// funcNameRE is the character set OpenAI allows in a function name. The host
// already sanitizes catalog names to this set; the check is kept because an
// unusable name must cost one tool, not every request.
var funcNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// catalog is the set of functions offered to the model for one generation of
// the tool source.
type catalog struct {
	defs []toolDef
	// offered is the set of function names the model may call, used to
	// reject a call to something that was never offered.
	offered map[string]bool
}

// buildCatalog converts the enabled MCP tools into function definitions.
//
// Each tool is offered under its catalog name, carrying its own input schema
// downgraded to the subset the completion endpoints accept. A tool that
// cannot be offered — an unusable name, or a schema that cannot be
// downgraded — is skipped with a warning: one bad tool from an external
// server must not make every request fail.
func buildCatalog(tools []*mcp.Tool, logger *slog.Logger) catalog {
	built := catalog{
		defs:    make([]toolDef, 0, len(tools)),
		offered: make(map[string]bool, len(tools)),
	}
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		if err := checkFuncName(tool.Name); err != nil {
			logger.Warn("tool not offered to the model", "tool", tool.Name, "error", err.Error())
			continue
		}
		if built.offered[tool.Name] {
			logger.Warn("tool not offered to the model", "tool", tool.Name, "error", "duplicate function name")
			continue
		}
		params, err := downgradeSchema(tool.InputSchema)
		if err != nil {
			logger.Warn("tool not offered to the model", "tool", tool.Name, "error", err.Error())
			continue
		}
		built.offered[tool.Name] = true
		built.defs = append(built.defs, toolDef{Type: "function", Function: functionDef{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  params,
		}})
	}
	return built
}

// checkFuncName rejects a tool name an endpoint cannot accept as a function
// name.
func checkFuncName(name string) error {
	if name == "" {
		return fmt.Errorf("tool has no name")
	}
	if len(name) > maxFuncNameLen {
		return fmt.Errorf("name exceeds %d characters", maxFuncNameLen)
	}
	if !funcNameRE.MatchString(name) {
		return fmt.Errorf("name is not a valid function name (allowed: letters, digits, '_', '-')")
	}
	return nil
}

// mustJSON marshals a schema value, panicking on failure. Only values built
// by this package reach it, so a failure is a programmer error.
func mustJSON(v any) json.RawMessage {
	buf, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("openai: marshal schema: %v", err))
	}
	return buf
}
