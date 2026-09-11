package mcphost

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxNameLen is the longest model-facing tool name the catalog emits. LLM
// tool-use APIs cap function names at 64 characters.
const maxNameLen = 64

// entry is one catalog entry: how to perform a call on the qualified name.
type entry struct {
	// host is set for a tool served by the host process itself.
	host *HostTool

	// session and name are set for a tool served by an MCP server; name is
	// the server-side name, before qualification.
	session *session
	name    string
}

// rebuild recomputes the catalog from the host tools and every session's
// cached tool list, and bumps the generation so cached catalogs downstream
// (a planner's function definitions, say) can tell that it changed.
//
// Host tools win over server tools of the same name, and an earlier server
// wins over a later one; a shadowed tool is dropped with a warning rather
// than silently taking over a name the model was already offered.
//
// It may run while New is still connecting servers, because a server can
// report its tools changed as soon as it is connected. The session list is
// therefore snapshotted under the lock, and New rebuilds once more when every
// server is in, so an early rebuild is at worst momentarily incomplete.
func (h *Host) rebuild() {
	catalog := make(map[string]entry)
	var ordered []*mcp.Tool

	names := make([]string, 0, len(h.hostTools))
	for name := range h.hostTools {
		names = append(names, name)
	}
	sort.Strings(names)
	// Host tools are not subject to the allowlist. They are what the host
	// itself can do rather than part of the operational surface an operator
	// curates, and the planner has no way to express a reply without the
	// reply tool: filtering it out would leave a bot that cannot answer at
	// all, since prose from the model becomes a reply step unconditionally.
	for _, name := range names {
		tool := h.hostTools[name]
		catalog[name] = entry{host: &tool}
		ordered = append(ordered, tool.Def)
	}

	for _, s := range h.snapshotSessions() {
		for _, tool := range s.snapshot() {
			qualified := qualify(s.alias, tool.Name)
			if existing, taken := catalog[qualified]; taken {
				h.logger.Warn("tool name already taken; tool not offered",
					"server", s.name, "tool", tool.Name,
					"name", qualified, "taken_by", sourceLabel(existing))
				continue
			}
			if !h.allowed(qualified) {
				continue
			}
			catalog[qualified] = entry{session: s, name: tool.Name}
			offered := *tool
			offered.Name = qualified
			ordered = append(ordered, &offered)
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.catalog = catalog
	h.ordered = ordered
	h.generation++
}

// sourceLabel names what already holds a catalog name, for the shadowing
// warning.
func sourceLabel(e entry) string {
	if e.host != nil {
		return "host"
	}
	return e.session.name
}

// allowed reports whether a qualified name passes the configured allowlist.
func (h *Host) allowed(name string) bool {
	if len(h.allow) == 0 {
		return true
	}
	for _, pattern := range h.allow {
		if ok, err := filepath.Match(pattern, name); err == nil && ok {
			return true
		}
	}
	return false
}

// CheckPatterns rejects malformed allowlist patterns.
//
// New applies it too, but it is exported so a caller can validate an
// operator's tool selection before opening any backend, rather than after a
// chat connection has already been established.
func CheckPatterns(patterns []string) error {
	for _, pattern := range patterns {
		if _, err := filepath.Match(pattern, ""); err != nil {
			return fmt.Errorf("mcphost: invalid tool pattern %q: %w", pattern, err)
		}
	}
	return nil
}

// qualify builds the model-facing name for one server's tool. The empty alias
// leaves a tool's own name unchanged, so the built-in tools keep the names
// operators already know.
func qualify(alias, name string) string {
	if alias != "" {
		name = alias + "-" + name
	}
	return sanitize(name)
}

// sanitize coerces a name into the character set LLM tool-use APIs accept
// (letters, digits, "_" and "-") and caps its length. A name that has to be
// shortened keeps a hash of the original so two long names cannot collapse
// onto each other.
func sanitize(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	cleaned := b.String()
	if len(cleaned) <= maxNameLen {
		return cleaned
	}
	sum := sha256.Sum256([]byte(name))
	suffix := "-" + hex.EncodeToString(sum[:4])
	return cleaned[:maxNameLen-len(suffix)] + suffix
}
