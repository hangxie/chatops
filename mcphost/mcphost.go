// Package mcphost connects a set of Model Context Protocol servers and
// presents their tools as one catalog.
//
// It is the client half of the tool boundary: operational tools are no longer
// a chatops-defined interface but MCP tools, served by MCP servers. Built-in
// groups run in process, each on its own goroutine, reached over the SDK's
// in-memory transport — which is a full JSON-RPC round trip over the same
// newline-delimited JSON codec the stdio transport uses, not a shortcut that
// passes Go values across. Nothing crosses the boundary except JSON, so any
// group can later be moved out of process without touching its code.
//
// A host also carries host tools: tools implemented in the process itself
// because they act on host state rather than on a remote endpoint (the reply
// tool posts into the live chat connection). They appear in the same catalog
// and are called the same way, exactly as an MCP host adds its own local tools
// to what it offers the model.
//
//	host, err := mcphost.New(ctx, mcphost.Config{
//		Servers:   []mcphost.ServerSpec{mcphost.InProcess("", srv)},
//		HostTools: []mcphost.HostTool{replyTool},
//	})
//	tools, err := host.Tools(ctx)
//	result, err := host.CallTool(ctx, "k8s-get", map[string]any{"kind": "pod", "name": "web-0"})
package mcphost

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hangxie/chatops/mcpserve"
)

// clientName identifies chatops to the servers it connects to.
const clientName = "chatops"

// ClientVersion is reported to servers as the host's implementation version.
const ClientVersion = "v0"

// ServerSpec describes one MCP server the host connects to.
//
// Transport is the client end of the connection. Serve, when non-nil, runs a
// server bound to the other end: the host starts it on its own goroutine
// before connecting and stops it on Close. An out-of-process server leaves
// Serve nil, which is the only difference between the two — the session, the
// catalog, and the calls are identical.
type ServerSpec struct {
	// Name identifies the server in logs and errors. It must be unique and
	// is never seen by the model.
	Name string

	// Alias prefixes this server's tool names in the catalog, keeping tools
	// from different servers apart. An empty alias leaves the server's tools
	// under their own names, which suits servers whose names are already
	// distinct; a name claimed twice is reported and offered once.
	Alias string

	Transport mcp.Transport
	Serve     func(ctx context.Context) error
}

// InProcess returns a ServerSpec running srv in this process, connected over
// an in-memory transport. Moving the same server out of process later is a
// change of ServerSpec at the wiring site and nothing more.
func InProcess(name, alias string, srv *mcp.Server) ServerSpec {
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	return ServerSpec{
		Name:      name,
		Alias:     alias,
		Transport: clientTransport,
		Serve:     func(ctx context.Context) error { return srv.Run(ctx, serverTransport) },
	}
}

// HostTool is a tool implemented in the host process rather than by a server.
type HostTool struct {
	// Def is the model-facing definition. Its name is used unqualified.
	Def *mcp.Tool

	// Handler performs the call. It reads any host state it needs from ctx
	// (see chat.WithConversation) rather than from arguments.
	Handler func(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error)
}

// Config describes the host to build.
type Config struct {
	// Servers are the MCP servers to connect. Aliases must be distinct.
	Servers []ServerSpec

	// HostTools are tools served by the host process itself. They take
	// precedence over server tools of the same name.
	HostTools []HostTool

	// Allow optionally restricts the catalog to tools whose qualified name
	// matches one of these patterns, in filepath.Match syntax. An empty Allow
	// exposes every tool.
	Allow []string

	// ListTimeout bounds one tools/list request. Zero uses
	// DefaultListTimeout. It applies to every listing, including the
	// refreshes a server triggers by reporting its tools changed, which have
	// no caller to cancel them.
	ListTimeout time.Duration

	// ConnectTimeout bounds one server's handshake. Zero uses
	// DefaultConnectTimeout.
	//
	// It is a hard bound rather than a courtesy: a transport whose peer
	// accepts the connection but never answers leaves the handshake blocked
	// with no way to interrupt it, so without a timeout one unresponsive
	// server would hang startup indefinitely.
	ConnectTimeout time.Duration

	// Logger receives structured records about server sessions and catalog
	// changes. A nil Logger discards all records.
	Logger *slog.Logger
}

const (
	// DefaultConnectTimeout is the default bound on one server's handshake.
	DefaultConnectTimeout = 30 * time.Second

	// DefaultListTimeout is the default bound on one tools/list request.
	DefaultListTimeout = 30 * time.Second
)

// Host is a connected set of MCP servers presented as one tool catalog.
type Host struct {
	logger         *slog.Logger
	allow          []string
	hostTools      map[string]HostTool
	connectTimeout time.Duration
	listTimeout    time.Duration
	sessions       []*session

	// rebuildMu serializes catalog rebuilds, so a slower one cannot publish
	// an older view after a faster one has published a newer.
	rebuildMu sync.Mutex

	// runCtx bounds in-process servers and outlives the call to New, which
	// only bounds connecting.
	runCtx  context.Context
	cancel  context.CancelFunc
	serving sync.WaitGroup

	mu         sync.RWMutex
	catalog    map[string]entry
	ordered    []*mcp.Tool
	generation uint64
	closed     bool
}

// New connects every configured server and builds the initial catalog. ctx
// bounds connecting only; the servers themselves live until Close.
func New(ctx context.Context, cfg Config) (*Host, error) {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	hostTools, err := indexHostTools(cfg.HostTools)
	if err != nil {
		return nil, err
	}
	if err := checkServers(cfg.Servers); err != nil {
		return nil, err
	}
	if err := CheckPatterns(cfg.Allow); err != nil {
		return nil, err
	}

	timeout := cfg.ConnectTimeout
	if timeout <= 0 {
		timeout = DefaultConnectTimeout
	}
	listTimeout := cfg.ListTimeout
	if listTimeout <= 0 {
		listTimeout = DefaultListTimeout
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	h := &Host{
		logger:         logger,
		allow:          append([]string(nil), cfg.Allow...),
		hostTools:      hostTools,
		connectTimeout: timeout,
		listTimeout:    listTimeout,
		runCtx:         runCtx,
		cancel:         cancel,
	}
	for _, spec := range cfg.Servers {
		s, err := h.connect(ctx, spec)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("mcphost: connect server %q: %w", spec.Name, err), h.Close())
		}
		// A connected server may report its tools changed at any moment, and
		// that notification rebuilds the catalog from this slice, so it is
		// published under the lock rather than appended to in the open.
		h.mu.Lock()
		h.sessions = append(h.sessions, s)
		h.mu.Unlock()
	}
	h.rebuild()
	return h, nil
}

// snapshotSessions returns the sessions connected so far.
func (h *Host) snapshotSessions() []*session {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return append([]*session(nil), h.sessions...)
}

// connect starts a server's in-process goroutine, if it has one, and opens
// the client session.
func (h *Host) connect(ctx context.Context, spec ServerSpec) (*session, error) {
	if spec.Serve != nil {
		h.serving.Add(1)
		go func() {
			defer h.serving.Done()
			// A server stops when the host closes its transport or cancels
			// its context, which is what shutdown looks like from here.
			if err := spec.Serve(h.runCtx); !mcpserve.GracefulStop(err) {
				h.logger.Error("in-process server stopped", "server", spec.Name, "error", err.Error())
			}
		}()
	}
	connectCtx, cancel := context.WithTimeout(ctx, h.connectTimeout)
	defer cancel()
	return newSession(connectCtx, spec, h.listTimeout, h.logger, h.rebuild)
}

// indexHostTools validates the host tools and keys them by name.
func indexHostTools(tools []HostTool) (map[string]HostTool, error) {
	indexed := make(map[string]HostTool, len(tools))
	for _, t := range tools {
		if t.Def == nil || t.Def.Name == "" {
			return nil, errors.New("mcphost: host tool with no definition")
		}
		if t.Handler == nil {
			return nil, fmt.Errorf("mcphost: host tool %q has no handler", t.Def.Name)
		}
		if _, dup := indexed[t.Def.Name]; dup {
			return nil, fmt.Errorf("mcphost: duplicate host tool %q", t.Def.Name)
		}
		indexed[t.Def.Name] = t
	}
	return indexed, nil
}

// checkServers rejects a server list that cannot be connected: a server with
// no name or transport, or two servers sharing a name or a non-empty alias.
// The empty alias may be shared — servers whose tool names are already
// distinct need no prefix.
func checkServers(specs []ServerSpec) error {
	names := make(map[string]bool, len(specs))
	aliases := make(map[string]bool, len(specs))
	for _, spec := range specs {
		if spec.Name == "" {
			return errors.New("mcphost: server with no name")
		}
		if spec.Transport == nil {
			return fmt.Errorf("mcphost: server %q has no transport", spec.Name)
		}
		if names[spec.Name] {
			return fmt.Errorf("mcphost: duplicate server name %q", spec.Name)
		}
		names[spec.Name] = true
		if spec.Alias == "" {
			continue
		}
		if aliases[spec.Alias] {
			return fmt.Errorf("mcphost: duplicate server alias %q", spec.Alias)
		}
		aliases[spec.Alias] = true
	}
	return nil
}

// Close stops every session and every in-process server. It is idempotent.
func (h *Host) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	sessions := append([]*session(nil), h.sessions...)
	h.mu.Unlock()

	var errs []error
	for _, s := range sessions {
		if err := s.close(); err != nil {
			errs = append(errs, err)
		}
	}
	h.cancel()
	h.serving.Wait()
	return errors.Join(errs...)
}
