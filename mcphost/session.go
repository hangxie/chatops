package mcphost

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// session is one connected MCP server: the client session plus the cached
// tool list, refreshed when the server reports it changed.
type session struct {
	name   string
	alias  string
	logger *slog.Logger

	// listTimeout bounds one tools/list. A server that accepts the request
	// and never answers would otherwise block a refresh forever, and a
	// refresh triggered by a server notification has no caller to cancel it.
	listTimeout time.Duration

	// refreshMu serializes refreshes, so a slow listing cannot finish after a
	// later one and overwrite a newer tool list with an older one.
	refreshMu sync.Mutex

	mu     sync.RWMutex
	client *mcp.ClientSession
	tools  []*mcp.Tool
	// listErr records why the last tool listing failed. A server whose tools
	// cannot be listed contributes nothing to the catalog rather than taking
	// the host down with it.
	listErr error
}

// peer returns the connected session, or nil before it is installed.
func (s *session) peer() *mcp.ClientSession {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.client
}

// install publishes the connected session, after which notifications may act
// on it.
func (s *session) install(peer *mcp.ClientSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.client = peer
}

// listFailure returns why the last listing failed, or nil.
func (s *session) listFailure() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.listErr
}

// newSession connects to one server and fetches its initial tool list.
// onChange is called after the tool list changes, so the host can rebuild its
// catalog.
func newSession(ctx context.Context, spec ServerSpec, listTimeout time.Duration, logger *slog.Logger, onChange func()) (*session, error) {
	s := &session{name: spec.Name, alias: spec.Alias, logger: logger, listTimeout: listTimeout}
	client := mcp.NewClient(&mcp.Implementation{Name: clientName, Version: ClientVersion}, &mcp.ClientOptions{
		ToolListChangedHandler: func(ctx context.Context, _ *mcp.ToolListChangedRequest) {
			s.toolsChanged(ctx, onChange)
		},
	})
	sess, err := connect(ctx, client, spec.Transport)
	if err != nil {
		return nil, err
	}
	s.install(sess)
	s.refresh(ctx)
	if err := s.listFailure(); err != nil {
		return nil, errors.Join(err, sess.Close())
	}
	return s, nil
}

// connect performs the handshake under ctx.
//
// Client.Connect blocks in the handshake until the peer answers and does not
// itself honor ctx, so a server that accepts the connection and then goes
// quiet would block forever. The handshake therefore runs on its own
// goroutine and ctx bounds the wait; a session that arrives after the wait is
// closed rather than leaked.
func connect(ctx context.Context, client *mcp.Client, transport mcp.Transport) (*mcp.ClientSession, error) {
	type connected struct {
		session *mcp.ClientSession
		err     error
	}
	// Buffered, so the handshake goroutine never blocks on a send nobody is
	// waiting for any more.
	done := make(chan connected, 1)
	go func() {
		session, err := client.Connect(context.WithoutCancel(ctx), transport, nil)
		done <- connected{session: session, err: err}
	}()
	select {
	case result := <-done:
		return result.session, result.err
	case <-ctx.Done():
		go func() {
			if result := <-done; result.session != nil {
				_ = result.session.Close()
			}
		}()
		return nil, fmt.Errorf("handshake did not complete: %w", ctx.Err())
	}
}

// toolsChanged handles a server's report that its tools changed: it refreshes
// the cache and tells the host to rebuild its catalog.
//
// The notification handler has to be installed before the session exists,
// because the client must be built in order to connect it. A server that
// announces a change in that window finds no session to act on, so the
// notification is dropped rather than dereferencing a nil peer; newSession
// lists the tools itself once the session is installed, so the change is
// picked up anyway.
func (s *session) toolsChanged(ctx context.Context, onChange func()) {
	if s.peer() == nil {
		return
	}
	s.refresh(ctx)
	onChange()
}

// refresh re-fetches the server's tool list into the cache.
func (s *session) refresh(ctx context.Context) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, s.listTimeout)
	defer cancel()
	tools, err := s.listTools(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.listErr = err
		s.tools = nil
		s.logger.Error("listing server tools failed", "server", s.name, "error", err.Error())
		return
	}
	s.listErr = nil
	s.tools = tools
}

// listTools pages through the server's full tool list.
func (s *session) listTools(ctx context.Context) ([]*mcp.Tool, error) {
	var tools []*mcp.Tool
	for tool, err := range s.peer().Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("list tools: %w", err)
		}
		tools = append(tools, tool)
	}
	return tools, nil
}

// snapshot returns the cached tool list.
func (s *session) snapshot() []*mcp.Tool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]*mcp.Tool(nil), s.tools...)
}

// call invokes one tool by its server-side name.
func (s *session) call(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error) {
	return s.peer().CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
}

// close ends the session. The in-process server, if any, is stopped by the
// host cancelling its context.
func (s *session) close() error {
	if err := s.peer().Close(); err != nil {
		return fmt.Errorf("mcphost: close server %q: %w", s.name, err)
	}
	return nil
}
