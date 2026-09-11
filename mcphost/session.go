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

	// listTimeout bounds one tools/list.
	//
	// Waiting for a response honours its context, so this is not working
	// around the SDK: it is there because a refresh triggered by a server
	// notification arrives with no caller and so no deadline of its own, and
	// would wait forever on a server that accepted the request and went
	// quiet.
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

// sessionConfig is what newSession needs to bring up one server session.
type sessionConfig struct {
	spec ServerSpec

	// connectTimeout bounds the handshake and listTimeout bounds the initial
	// listing. They are separate budgets: a slow handshake must not eat into
	// the time allowed for listing, which is a second round trip to the same
	// server and was configured on its own.
	connectTimeout time.Duration
	listTimeout    time.Duration

	logger *slog.Logger

	// onChange is called after the tool list changes, so the host can rebuild
	// its catalog.
	onChange func()
}

// newSession connects to one server and fetches its initial tool list.
func newSession(ctx context.Context, cfg sessionConfig) (*session, error) {
	spec, logger, onChange := cfg.spec, cfg.logger, cfg.onChange
	s := &session{name: spec.Name, alias: spec.Alias, logger: logger, listTimeout: cfg.listTimeout}
	client := mcp.NewClient(&mcp.Implementation{Name: clientName, Version: ClientVersion}, &mcp.ClientOptions{
		ToolListChangedHandler: func(ctx context.Context, _ *mcp.ToolListChangedRequest) {
			s.toolsChanged(ctx, onChange)
		},
	})
	connectCtx, cancel := context.WithTimeout(ctx, cfg.connectTimeout)
	defer cancel()
	sess, err := connect(connectCtx, client, spec.Transport)
	if err != nil {
		return nil, err
	}
	s.install(sess)
	// The initial listing is bounded from the caller's context, not from what
	// the handshake left of its own: refresh derives its deadline from what it
	// is given, and a context that already carries an earlier one would cap it.
	s.refresh(ctx)
	if err := s.listFailure(); err != nil {
		return nil, errors.Join(err, sess.Close())
	}
	return s, nil
}

// errAbandoned reports a handshake given up on before it completed.
var errAbandoned = errors.New("handshake abandoned")

// handshakeTransport wraps a transport so the connection it opens can be
// closed from outside the handshake.
//
// The transport checks the context before writing and then performs a write
// that cannot be interrupted, so Client.Connect ignores cancellation for as
// long as that write is stuck — which is until the peer reads. An in-memory
// transport has no buffer, so a peer that never appears blocks it forever.
// Closing the connection is what unsticks the write; waiting for the answer
// that follows honours the context on its own.
type handshakeTransport struct {
	inner mcp.Transport

	mu        sync.Mutex
	conn      mcp.Connection
	abandoned bool
}

// Connect opens the wrapped transport and remembers the connection, so
// abandon can close it. A transport that opens after the handshake has
// already been abandoned is closed immediately rather than left running.
func (t *handshakeTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	conn, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.abandoned {
		return nil, errors.Join(errAbandoned, conn.Close())
	}
	t.conn = conn
	return conn, nil
}

// abandon closes the connection, unblocking a handshake that is waiting on a
// peer that will not answer.
func (t *handshakeTransport) abandon() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.abandoned = true
	if t.conn == nil {
		return nil
	}
	return t.conn.Close()
}

// connect performs the handshake under ctx.
//
// The handshake runs on its own goroutine so ctx can bound the wait, and on
// timeout the connection is closed so that goroutine finishes rather than
// blocking on a peer forever. The close is what matters: a handshake stuck
// writing to a peer that is not reading ignores cancellation, and without it
// both that goroutine and the one reaping it would leak for the life of the
// process — once per attempt, which compounds for a caller that retries.
func connect(ctx context.Context, client *mcp.Client, transport mcp.Transport) (*mcp.ClientSession, error) {
	handshake := &handshakeTransport{inner: transport}
	// Buffered, so the handshake goroutine never blocks on a send nobody is
	// waiting for any more.
	done := make(chan handshakeResult, 1)
	go func() {
		session, err := client.Connect(ctx, handshake, nil)
		done <- handshakeResult{session: session, err: err}
	}()
	select {
	case result := <-done:
		return result.session, result.err
	case <-ctx.Done():
		abandonErr := handshake.abandon()
		// Abandoning ends the handshake, so this goroutine completes rather
		// than blocking forever.
		go discardLateSession(done)
		return nil, errors.Join(fmt.Errorf("handshake did not complete: %w", ctx.Err()), abandonErr)
	}
}

// handshakeResult is the outcome of one handshake attempt.
type handshakeResult struct {
	session *mcp.ClientSession
	err     error
}

// discardLateSession closes a session that arrived after the wait for it was
// abandoned, so a handshake that won the race is not left connected.
//
// Closing the connection normally makes a late handshake fail, which leaves
// nothing to discard; this covers the narrow case where the handshake
// completed just as the wait gave up on it.
func discardLateSession(done <-chan handshakeResult) {
	if result := <-done; result.session != nil {
		_ = result.session.Close()
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
//
// A failed refresh keeps the tools already known rather than dropping them.
// Nothing retries: the only thing that starts another refresh is the server
// reporting its tools changed again, so clearing the cache on one timeout
// would remove that server from the catalog for as long as it stayed quiet —
// which, for a server whose tools are stable, is forever. The last known list
// is the best information available, and a call against a tool that has
// really gone fails at the server and is reported like any other failure.
//
// The initial listing is the exception: newSession has no previous list to
// keep and refuses to bring the session up at all.
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
		s.logger.Error("listing server tools failed; keeping the tools already known",
			"server", s.name, "tools", len(s.tools), "error", err.Error())
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
