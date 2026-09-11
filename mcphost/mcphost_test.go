package mcphost_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/mcphost"
)

// echoArgs is the input of the stub servers' echo tool.
type echoArgs struct {
	Text string `json:"text,omitempty"`
}

// stubServer serves the named tools, each echoing its own name and any text
// argument, so a test can tell which server answered.
func stubServer(name string, tools ...string) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: name, Version: "v0"}, nil)
	for _, tool := range tools {
		addEcho(srv, name, tool)
	}
	return srv
}

func addEcho(srv *mcp.Server, server, tool string) {
	mcp.AddTool(srv, &mcp.Tool{Name: tool, Description: "Echo from " + server},
		func(_ context.Context, _ *mcp.CallToolRequest, args echoArgs) (*mcp.CallToolResult, any, error) {
			text := server + "/" + tool
			if args.Text != "" {
				text += ":" + args.Text
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
		})
}

// newHost builds a host and closes it when the test ends.
func newHost(t *testing.T, cfg mcphost.Config) *mcphost.Host {
	t.Helper()
	host, err := mcphost.New(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, host.Close()) })
	return host
}

// hostTool builds a host tool recording that it ran.
func hostTool(name string, called *atomic.Bool) mcphost.HostTool {
	return mcphost.HostTool{
		Def: &mcp.Tool{Name: name, Description: "A host tool."},
		Handler: func(context.Context, map[string]any) (*mcp.CallToolResult, error) {
			called.Store(true)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "host/" + name}}}, nil
		},
	}
}

func Test_New_rejects_bad_config(t *testing.T) {
	testCases := map[string]struct {
		cfg    mcphost.Config
		errMsg string
	}{
		"no-server-name": {
			cfg:    mcphost.Config{Servers: []mcphost.ServerSpec{{Transport: &mcp.InMemoryTransport{}}}},
			errMsg: "server with no name",
		},
		"no-transport": {
			cfg:    mcphost.Config{Servers: []mcphost.ServerSpec{{Name: "alpha"}}},
			errMsg: `server "alpha" has no transport`,
		},
		"duplicate-name": {
			cfg: mcphost.Config{Servers: []mcphost.ServerSpec{
				mcphost.InProcess("alpha", "", stubServer("alpha", "one")),
				mcphost.InProcess("alpha", "x", stubServer("alpha", "two")),
			}},
			errMsg: `duplicate server name "alpha"`,
		},
		"duplicate-alias": {
			cfg: mcphost.Config{Servers: []mcphost.ServerSpec{
				mcphost.InProcess("alpha", "shared", stubServer("alpha", "one")),
				mcphost.InProcess("beta", "shared", stubServer("beta", "two")),
			}},
			errMsg: `duplicate server alias "shared"`,
		},
		"host-tool-no-def": {
			cfg:    mcphost.Config{HostTools: []mcphost.HostTool{{}}},
			errMsg: "host tool with no definition",
		},
		"host-tool-no-handler": {
			cfg:    mcphost.Config{HostTools: []mcphost.HostTool{{Def: &mcp.Tool{Name: "reply"}}}},
			errMsg: `host tool "reply" has no handler`,
		},
		"duplicate-host-tool": {
			cfg: mcphost.Config{HostTools: []mcphost.HostTool{
				{Def: &mcp.Tool{Name: "reply"}, Handler: noopHandler},
				{Def: &mcp.Tool{Name: "reply"}, Handler: noopHandler},
			}},
			errMsg: `duplicate host tool "reply"`,
		},
		"bad-pattern": {
			cfg:    mcphost.Config{Allow: []string{"[bad"}},
			errMsg: `invalid tool pattern "[bad"`,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			host, err := mcphost.New(context.Background(), tc.cfg)
			require.ErrorContains(t, err, tc.errMsg)
			require.Nil(t, host)
		})
	}
}

func noopHandler(context.Context, map[string]any) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{}, nil
}

// failingConnectTransport cannot be connected at all.
type failingConnectTransport struct{ err error }

func (t failingConnectTransport) Connect(context.Context) (mcp.Connection, error) {
	return nil, t.err
}

func Test_New_fails_when_a_server_cannot_be_connected(t *testing.T) {
	connectErr := errors.New("no such server")

	// A server that cannot be reached at startup is fatal to New: the
	// operator asked for it, so serving without it would silently drop tools.
	// Sessions already opened are closed on the way out.
	host, err := mcphost.New(context.Background(), mcphost.Config{
		Servers: []mcphost.ServerSpec{
			mcphost.InProcess("alpha", "", stubServer("alpha", "one")),
			{Name: "beta", Transport: failingConnectTransport{err: connectErr}},
		},
	})
	require.Nil(t, host)
	require.ErrorIs(t, err, connectErr)
	require.ErrorContains(t, err, `connect server "beta"`)
}

func Test_Close_is_idempotent(t *testing.T) {
	host, err := mcphost.New(context.Background(), mcphost.Config{
		Servers: []mcphost.ServerSpec{mcphost.InProcess("alpha", "", stubServer("alpha", "one"))},
	})
	require.NoError(t, err)

	require.NoError(t, host.Close())
	require.NoError(t, host.Close())
}

func Test_Close_reports_a_failing_session(t *testing.T) {
	closeErr := errors.New("close refused")
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	srv := stubServer("alpha", "one")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Run(ctx, serverTransport) }()

	host, err := mcphost.New(context.Background(), mcphost.Config{
		Servers: []mcphost.ServerSpec{{
			Name:      "alpha",
			Transport: failingCloseTransport{inner: clientTransport, err: closeErr},
		}},
	})
	require.NoError(t, err)

	// One failed cleanup is reported rather than swallowed, and Close still
	// completes.
	require.ErrorIs(t, host.Close(), closeErr)
}

func Test_in_process_server_failure_is_logged(t *testing.T) {
	var logs bytes.Buffer
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	srv := stubServer("alpha", "one")
	stopped := make(chan struct{})

	host, err := mcphost.New(context.Background(), mcphost.Config{
		Servers: []mcphost.ServerSpec{{
			Name:      "alpha",
			Transport: clientTransport,
			Serve: func(ctx context.Context) error {
				_ = srv.Run(ctx, serverTransport)
				close(stopped)
				// A server that fails for its own reasons, rather than
				// because the host shut it down, must be reported.
				return errors.New("server crashed")
			},
		}},
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})
	require.NoError(t, err)
	require.Equal(t, []string{"one"}, host.Names())

	require.NoError(t, host.Close())
	<-stopped
	require.Contains(t, logs.String(), "in-process server stopped")
	require.Contains(t, logs.String(), "server crashed")
}

// failingCloseTransport wraps a transport so the resulting connection fails to
// close.
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

// triggerTransport runs a callback while the host is connecting it, which is
// the moment New has connected the earlier servers and has not yet finished
// with this one.
type triggerTransport struct {
	inner   mcp.Transport
	trigger func()
}

func (t triggerTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	conn, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	t.trigger()
	return conn, nil
}

// Test_New_tolerates_notifications_during_connect drives a rebuild from an
// already-connected server while New is still connecting a later one, and
// checks that the change announced in that window reaches the catalog.
//
// It is not a reproducer for the data race that motivated publishing the
// session list under the lock: the overlap needed for the detector to fire is
// narrower than this can reliably provoke. It covers the behaviour, not the
// synchronization.
func Test_New_tolerates_notifications_during_connect(t *testing.T) {
	busy := make([]string, 0, 400)
	for i := range 400 {
		busy = append(busy, fmt.Sprintf("tool%03d", i))
	}
	first := stubServer("first", busy...)
	firstSpec := mcphost.InProcess("first", "", first)

	secondServerTransport, secondClientTransport := mcp.NewInMemoryTransports()
	second := stubServer("second", "one")
	secondSpec := mcphost.ServerSpec{
		Name: "second",
		Transport: triggerTransport{inner: secondClientTransport, trigger: func() {
			// Make the first server announce a change, then give its handler
			// a head start so the rebuild is in flight as New returns here
			// and appends this session.
			addEcho(first, "first", "late")
			time.Sleep(2 * time.Millisecond)
		}},
		Serve: func(ctx context.Context) error { return second.Run(ctx, secondServerTransport) },
	}

	host, err := mcphost.New(context.Background(), mcphost.Config{Servers: []mcphost.ServerSpec{firstSpec, secondSpec}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, host.Close()) })

	// Both servers are in the catalog however the rebuilds interleaved, and
	// the change announced mid-connect is not lost. It converges rather than
	// landing synchronously: a notification is handled on its own goroutine.
	require.Contains(t, host.Names(), "tool000")
	require.Contains(t, host.Names(), "one")
	require.Eventually(t, func() bool {
		return slices.Contains(host.Names(), "late")
	}, 3*time.Second, 5*time.Millisecond)
}

func Test_New_empty(t *testing.T) {
	host := newHost(t, mcphost.Config{})

	require.Empty(t, host.Names())
	require.Empty(t, host.Tools(context.Background()))
}
