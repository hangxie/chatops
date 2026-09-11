package mcphost

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// echoServer serves one argument-less tool per name.
func echoServer(name string, tools ...string) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: name, Version: "v0"}, nil)
	for _, tool := range tools {
		mcp.AddTool(srv, &mcp.Tool{Name: tool, Description: "echo"},
			func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: name}}}, nil, nil
			})
	}
	return srv
}

// serve runs srv on its own goroutine against one end of a fresh in-memory
// pair and returns the client end.
//
// Server.Run returns only once the connection ends, not when its context is
// cancelled, so cleanup cancels without waiting for it.
func serve(t *testing.T, srv *mcp.Server) mcp.Transport {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Run(ctx, serverTransport) }()
	t.Cleanup(cancel)
	return clientTransport
}

// listFailer makes tools/list fail on demand, so the listing error paths can
// be exercised against a server that is otherwise healthy and reachable.
type listFailer struct {
	failing atomic.Bool
}

func (f *listFailer) middleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/list" && f.failing.Load() {
				return nil, errors.New("listing refused")
			}
			return next(ctx, method, req)
		}
	}
}

// newTestSession connects a session to srv, failing the test if it cannot.
func newTestSession(t *testing.T, srv *mcp.Server, logger *slog.Logger) *session {
	t.Helper()
	s, err := newSession(context.Background(), ServerSpec{Name: "alpha", Transport: serve(t, srv)},
		time.Second, logger, func() {})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.close() })
	return s
}

func Test_newSession_fails_when_tools_cannot_be_listed(t *testing.T) {
	var logs bytes.Buffer
	srv := echoServer("alpha", "one")
	failer := &listFailer{}
	failer.failing.Store(true)
	srv.AddReceivingMiddleware(failer.middleware())

	// A server that connects but cannot describe itself is useless, so the
	// session is rejected rather than joining the host with no tools.
	s, err := newSession(context.Background(), ServerSpec{Name: "alpha", Transport: serve(t, srv)},
		time.Second, slog.New(slog.NewTextHandler(&logs, nil)), func() {})
	require.Error(t, err)
	require.ErrorContains(t, err, "listing refused")
	require.Nil(t, s)
	require.Contains(t, logs.String(), "listing server tools failed")
}

func Test_session_refresh_records_listing_failure(t *testing.T) {
	var logs bytes.Buffer
	srv := echoServer("alpha", "one")
	failer := &listFailer{}
	srv.AddReceivingMiddleware(failer.middleware())
	s := newTestSession(t, srv, slog.New(slog.NewTextHandler(&logs, nil)))
	require.Len(t, s.snapshot(), 1)

	// A server that stops answering contributes nothing rather than keeping
	// stale tools in the catalog, and the host stays up.
	failer.failing.Store(true)
	s.refresh(context.Background())

	require.Empty(t, s.snapshot())
	require.Error(t, s.listFailure())
	require.Contains(t, logs.String(), "listing server tools failed")
	require.Contains(t, logs.String(), "server=alpha")

	// Recovery is automatic: the next refresh restores the tools.
	failer.failing.Store(false)
	s.refresh(context.Background())
	require.Len(t, s.snapshot(), 1)
	require.NoError(t, s.listFailure())
}

func Test_session_refresh_bounds_a_silent_server(t *testing.T) {
	var logs bytes.Buffer
	srv := echoServer("alpha", "one")
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	listed := false
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			// Answer the first listing (the one newSession makes), then go
			// silent, as a wedged server would.
			if method == "tools/list" {
				if listed {
					<-block
					return nil, errors.New("unblocked")
				}
				listed = true
			}
			return next(ctx, method, req)
		}
	})
	s := newTestSession(t, srv, slog.New(slog.NewTextHandler(&logs, nil)))
	s.listTimeout = 100 * time.Millisecond

	// A refresh triggered by a server notification has no caller to cancel
	// it, so the listing must bound itself or the catalog would wedge.
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.refresh(context.Background())
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("refresh did not return; a silent server wedged it")
	}
	require.Error(t, s.listFailure())
}

func Test_session_listTools_error_is_wrapped(t *testing.T) {
	srv := echoServer("alpha", "one")
	failer := &listFailer{}
	srv.AddReceivingMiddleware(failer.middleware())
	s := newTestSession(t, srv, slog.New(slog.DiscardHandler))

	failer.failing.Store(true)
	_, err := s.listTools(context.Background())
	require.ErrorContains(t, err, "list tools:")
}

func Test_session_refreshes_on_tool_list_changed(t *testing.T) {
	srv := echoServer("alpha", "one")
	changed := make(chan struct{}, 4)
	s, err := newSession(context.Background(), ServerSpec{Name: "alpha", Transport: serve(t, srv)},
		time.Second, slog.New(slog.DiscardHandler), func() { changed <- struct{}{} })
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.close() })
	require.Len(t, s.snapshot(), 1)

	// Adding a tool to a connected server notifies the client, which
	// refreshes its cache and tells the host to rebuild: the catalog is live,
	// not frozen at connect.
	mcp.AddTool(srv, &mcp.Tool{Name: "two", Description: "echo"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{}, nil, nil
		})

	select {
	case <-changed:
	case <-time.After(3 * time.Second):
		t.Fatal("no catalog change reported")
	}
	require.Len(t, s.snapshot(), 2)
}

// unconnectableTransport cannot be connected at all.
type unconnectableTransport struct{ err error }

func (t unconnectableTransport) Connect(context.Context) (mcp.Connection, error) {
	return nil, t.err
}

func Test_newSession_reports_a_transport_that_cannot_connect(t *testing.T) {
	connectErr := errors.New("no such server")

	s, err := newSession(context.Background(),
		ServerSpec{Name: "alpha", Transport: unconnectableTransport{err: connectErr}},
		time.Second, slog.New(slog.DiscardHandler), func() {})
	require.ErrorIs(t, err, connectErr)
	require.Nil(t, s)
}

func Test_toolsChanged_before_install_is_dropped(t *testing.T) {
	// A session whose peer is not installed yet has nothing to list, so the
	// notification is dropped instead of dereferencing a nil client — and the
	// host is not asked to rebuild on the strength of it.
	rebuilt := false
	s := &session{name: "alpha", logger: slog.New(slog.DiscardHandler), listTimeout: time.Second}

	require.NotPanics(t, func() {
		s.toolsChanged(context.Background(), func() { rebuilt = true })
	})
	require.False(t, rebuilt)
	require.Empty(t, s.snapshot())
}

func Test_toolsChanged_after_install_refreshes(t *testing.T) {
	srv := echoServer("alpha", "one")
	s := newTestSession(t, srv, slog.New(slog.DiscardHandler))

	rebuilt := false
	mcp.AddTool(srv, &mcp.Tool{Name: "two", Description: "echo"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{}, nil, nil
		})
	s.toolsChanged(context.Background(), func() { rebuilt = true })

	require.True(t, rebuilt)
	require.Len(t, s.snapshot(), 2)
}

// Test_notification_before_install_is_dropped connects a server that
// announces tool-list changes from the moment it is connected, so the handler
// runs while the session is still being set up. Setting up must survive it
// and still end with a listed tool set.
//
// The nil-peer guard it motivates cannot be provoked on demand — the handler
// has to run in the gap between the handshake completing and the session
// being installed — so this covers the path, not the window.
func Test_notification_before_install_is_dropped(t *testing.T) {
	srv := echoServer("eager", "one")
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		serverSession, err := srv.Connect(ctx, serverTransport, nil)
		if err != nil {
			return
		}
		// Announce repeatedly, including during the client's own handshake.
		for range 20 {
			mcp.AddTool(srv, &mcp.Tool{Name: "two", Description: "echo"},
				func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
					return &mcp.CallToolResult{}, nil, nil
				})
			srv.RemoveTools("two")
		}
		<-ctx.Done()
		_ = serverSession.Close()
	}()

	s, err := newSession(context.Background(), ServerSpec{Name: "eager", Transport: clientTransport},
		time.Second, slog.New(slog.DiscardHandler), func() {})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.close() })
	require.NotEmpty(t, s.snapshot())
}

func Test_session_call_reaches_the_server(t *testing.T) {
	s := newTestSession(t, echoServer("alpha", "one"), slog.New(slog.DiscardHandler))

	result, err := s.call(context.Background(), "one", nil)
	require.NoError(t, err)
	require.Equal(t, "alpha", Render(result))
}

// failingCloseTransport wraps a transport so the resulting connection fails to
// close, exercising the session's close error path.
type failingCloseTransport struct {
	inner mcp.Transport
	err   error
}

func (t failingCloseTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	conn, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return failingCloseConn{Connection: conn, err: t.err}, nil
}

type failingCloseConn struct {
	mcp.Connection
	err error
}

func (c failingCloseConn) Close() error {
	_ = c.Connection.Close()
	return c.err
}

func Test_session_close_wraps_error(t *testing.T) {
	closeErr := errors.New("close refused")
	s, err := newSession(context.Background(),
		ServerSpec{Name: "alpha", Transport: failingCloseTransport{inner: serve(t, echoServer("alpha", "one")), err: closeErr}},
		time.Second, slog.New(slog.DiscardHandler), func() {})
	require.NoError(t, err)

	err = s.close()
	require.ErrorIs(t, err, closeErr)
	require.ErrorContains(t, err, `close server "alpha"`)
}

func Test_connect_closes_a_session_that_arrives_after_the_deadline(t *testing.T) {
	srv := echoServer("slow", "one")
	// Closing tracks whether the late session was cleaned up rather than
	// left connected.
	closed := make(chan struct{})
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			return next(ctx, method, req)
		}
	})
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		// The peer appears only after the deadline has passed, so the
		// handshake completes with nobody waiting for it.
		time.Sleep(100 * time.Millisecond)
		_ = srv.Run(ctx, serverTransport)
		close(closed)
	}()

	connectCtx, connectCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer connectCancel()
	client := mcp.NewClient(&mcp.Implementation{Name: clientName, Version: ClientVersion}, nil)
	session, err := connect(connectCtx, client, clientTransport)
	require.Nil(t, session)
	require.ErrorContains(t, err, "handshake did not complete")
	require.ErrorIs(t, err, context.DeadlineExceeded)

	// The late session is closed rather than leaked, which ends the server's
	// Run without anyone cancelling it.
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("late session was not closed")
	}
}
