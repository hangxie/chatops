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
	client *mcp.ClientSession

	// listTimeout bounds one tools/list. A server that accepts the request
	// and never answers would otherwise block a refresh forever, and a
	// refresh triggered by a server notification has no caller to cancel it.
	listTimeout time.Duration

	mu    sync.RWMutex
	tools []*mcp.Tool
	// listErr records why the last tool listing failed. A server whose tools
	// cannot be listed contributes nothing to the catalog rather than taking
	// the host down with it.
	listErr error
}

// newSession connects to one server and fetches its initial tool list.
// onChange is called after the tool list changes, so the host can rebuild its
// catalog.
func newSession(ctx context.Context, spec ServerSpec, listTimeout time.Duration, logger *slog.Logger, onChange func()) (*session, error) {
	s := &session{name: spec.Name, alias: spec.Alias, logger: logger, listTimeout: listTimeout}
	client := mcp.NewClient(&mcp.Implementation{Name: clientName, Version: ClientVersion}, &mcp.ClientOptions{
		ToolListChangedHandler: func(ctx context.Context, _ *mcp.ToolListChangedRequest) {
			s.refresh(ctx)
			onChange()
		},
	})
	sess, err := connect(ctx, client, spec.Transport)
	if err != nil {
		return nil, err
	}
	s.client = sess
	s.refresh(ctx)
	if s.listErr != nil {
		return nil, errors.Join(s.listErr, sess.Close())
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

// refresh re-fetches the server's tool list into the cache.
func (s *session) refresh(ctx context.Context) {
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
	for tool, err := range s.client.Tools(ctx, nil) {
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
	return s.client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
}

// close ends the session. The in-process server, if any, is stopped by the
// host cancelling its context.
func (s *session) close() error {
	if err := s.client.Close(); err != nil {
		return fmt.Errorf("mcphost: close server %q: %w", s.name, err)
	}
	return nil
}
