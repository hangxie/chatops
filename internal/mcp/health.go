package mcp

import mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

// ServerHealth is one MCP server's connection state and how many of its tools the catalog holds.
type ServerHealth struct {
	ID    string
	Up    bool
	Tools int
}

// Health reports every configured server in ID order; with no reconnect yet, a server stays down once its session ends.
func (m *Manager) Health() []ServerHealth {
	tools := map[string]int{}
	for _, tool := range m.Catalog().Tools() {
		tools[tool.Server]++
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := sortedIDs(m.listed)
	health := make([]ServerHealth, 0, len(ids))
	for _, id := range ids {
		health = append(health, ServerHealth{ID: id, Up: !m.closed && !m.down[id], Tools: tools[id]})
	}
	return health
}

// watch marks a server down when its session ends; Close waits for every watcher.
func (m *Manager) watch(id string, session *mcpsdk.ClientSession) {
	m.watchers.Add(1)
	go func() {
		defer m.watchers.Done()
		err := session.Wait()
		m.mu.Lock()
		defer m.mu.Unlock()
		if !m.closed {
			m.logger.Warn("mcp server disconnected", "server", id, "error", err)
		}
		m.down[id] = true
	}()
}
