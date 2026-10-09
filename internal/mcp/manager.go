// Package mcp connects to configured MCP servers and exposes their tools under model-facing names.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hangxie/chatops/internal/config"
	"github.com/hangxie/chatops/internal/version"
)

// Manager holds one session per server and a catalog rebuilt on tool-list changes.
type Manager struct {
	logger  *slog.Logger
	catalog atomic.Pointer[Catalog]

	// refreshCtx bounds background catalog refreshes; Close cancels it.
	refreshCtx context.Context
	cancel     context.CancelFunc
	refreshes  sync.WaitGroup
	// refreshMu serializes refreshes so a stale listing never overwrites a newer one.
	refreshMu sync.Mutex

	mu       sync.Mutex
	closed   bool
	sessions map[string]*mcpsdk.ClientSession
	listed   map[string][]*mcpsdk.Tool
}

// Open connects to every configured server and fails if any is unreachable.
func Open(ctx context.Context, servers map[string]config.Server, logger *slog.Logger) (*Manager, error) {
	transports := make(map[string]mcpsdk.Transport, len(servers))
	for _, id := range sortedIDs(servers) {
		transport, err := newTransport(servers[id])
		if err != nil {
			return nil, fmt.Errorf("mcp server %q: %w", id, err)
		}
		transports[id] = transport
	}
	return connect(ctx, transports, logger)
}

func connect(ctx context.Context, transports map[string]mcpsdk.Transport, logger *slog.Logger) (*Manager, error) {
	if logger == nil {
		logger = slog.Default()
	}
	m := &Manager{
		logger:   logger,
		sessions: map[string]*mcpsdk.ClientSession{},
		listed:   map[string][]*mcpsdk.Tool{},
	}
	m.refreshCtx, m.cancel = context.WithCancel(context.Background())

	for _, id := range sortedIDs(transports) {
		client := mcpsdk.NewClient(
			&mcpsdk.Implementation{Name: "chatops", Version: version.String()},
			&mcpsdk.ClientOptions{
				ToolListChangedHandler: func(context.Context, *mcpsdk.ToolListChangedRequest) { m.scheduleRefresh(id) },
			},
		)
		session, err := client.Connect(ctx, transports[id], nil)
		if err != nil {
			_ = m.Close()
			return nil, fmt.Errorf("mcp server %q: connect: %w", id, err)
		}
		tools, err := listTools(ctx, session)
		m.mu.Lock()
		m.sessions[id] = session
		m.listed[id] = tools
		m.mu.Unlock()
		if err != nil {
			_ = m.Close()
			return nil, fmt.Errorf("mcp server %q: list tools: %w", id, err)
		}
	}

	m.mu.Lock()
	m.rebuildLocked()
	m.mu.Unlock()
	return m, nil
}

// Catalog returns the current snapshot; callers keep one snapshot for a whole turn.
func (m *Manager) Catalog() *Catalog {
	return m.catalog.Load()
}

// Call validates args against a Catalog tool's schema, then invokes it; callers must authorize first.
// Tool failures return a Result with IsError; protocol and connection failures return an error.
func (m *Manager) Call(ctx context.Context, tool Tool, args json.RawMessage) (Result, error) {
	arguments, err := tool.arguments(args)
	if err != nil {
		return Result{}, fmt.Errorf("call %s/%s: invalid arguments: %w", tool.Server, tool.MCPName, err)
	}

	m.mu.Lock()
	session := m.sessions[tool.Server]
	m.mu.Unlock()
	if session == nil {
		return Result{}, fmt.Errorf("call %s/%s: server %q is not connected", tool.Server, tool.MCPName, tool.Server)
	}

	result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: tool.MCPName, Arguments: arguments})
	if err != nil {
		return Result{}, fmt.Errorf("call %s/%s: %w", tool.Server, tool.MCPName, err)
	}
	return newResult(result), nil
}

// Close ends every session and waits for background refreshes to stop.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	sessions := m.sessions
	m.sessions = map[string]*mcpsdk.ClientSession{}
	m.mu.Unlock()

	m.cancel()
	var errs []error
	for _, id := range sortedIDs(sessions) {
		if err := sessions[id].Close(); err != nil {
			errs = append(errs, fmt.Errorf("mcp server %q: close: %w", id, err))
		}
	}
	m.refreshes.Wait()
	return errors.Join(errs...)
}

// scheduleRefresh runs off the notification handler, where listing tools would block the session.
func (m *Manager) scheduleRefresh(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.refreshes.Add(1)
	go func() {
		defer m.refreshes.Done()
		m.refresh(id)
	}()
}

func (m *Manager) refresh(id string) {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()

	m.mu.Lock()
	session := m.sessions[id]
	m.mu.Unlock()
	if session == nil {
		return
	}
	tools, err := listTools(m.refreshCtx, session)
	if err != nil {
		m.logger.Warn("refresh mcp tool list", "server", id, "error", err)
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.listed[id] = tools
	m.rebuildLocked()
}

func (m *Manager) rebuildLocked() {
	catalog, skipped := NewCatalog(m.listed)
	for _, err := range skipped {
		m.logger.Warn("skip mcp tool", "error", err)
	}
	m.catalog.Store(catalog)
}

func listTools(ctx context.Context, session *mcpsdk.ClientSession) ([]*mcpsdk.Tool, error) {
	var tools []*mcpsdk.Tool
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return nil, err
		}
		tools = append(tools, tool)
	}
	return tools, nil
}

func sortedIDs[V any](m map[string]V) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}
