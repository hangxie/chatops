// Package serve implements `chatops-mcp serve`, which exposes the selected
// tool groups over stdio or Streamable HTTP.
package serve

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hangxie/chatops/internal/version"
	"github.com/hangxie/chatops/pkg/mcpserver/ping"
)

// groups maps tool group names accepted by --tools to their registration.
var groups = map[string]func(*mcp.Server){
	"ping": ping.Register,
}

// Cmd is the kong command for serve.
type Cmd struct {
	Tools    []string `help:"Comma-separated tool groups to serve (available: ping)." default:"ping" sep:","`
	HTTP     string   `help:"Serve Streamable HTTP at /mcp on this address (e.g. 127.0.0.1:8080) instead of stdio." placeholder:"ADDR"`
	TokenEnv string   `help:"Environment variable holding the bearer token HTTP clients must send; required when --http is not a loopback address." placeholder:"NAME"`

	// transport overrides stdio and listener overrides --http listening in tests.
	transport mcp.Transport
	listener  net.Listener
}

// Run serves MCP until the client disconnects or ctx is cancelled.
func (c Cmd) Run(ctx context.Context) error {
	server, err := newServer(c.Tools)
	if err != nil {
		return err
	}
	if c.HTTP != "" {
		return c.serveHTTP(ctx, server)
	}
	if c.TokenEnv != "" {
		return errors.New("--token-env requires --http")
	}
	transport := c.transport
	if transport == nil {
		transport = &mcp.StdioTransport{}
	}
	// Cancellation is a normal stop; any other error is reported even during shutdown.
	if err := server.Run(ctx, transport); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("serve stdio: %w", err)
	}
	return nil
}

func (c Cmd) serveHTTP(ctx context.Context, server *mcp.Server) error {
	token, err := bearerToken(c.TokenEnv)
	if err != nil {
		return err
	}
	if err := checkBind(c.HTTP, token); err != nil {
		return err
	}
	listener := c.listener
	if listener == nil {
		if listener, err = net.Listen("tcp", c.HTTP); err != nil {
			return fmt.Errorf("serve http: %w", err)
		}
	}
	return serveHTTP(ctx, listener, httpHandler(server, token))
}

func newServer(names []string) (*mcp.Server, error) {
	if len(names) == 0 {
		return nil, errors.New("at least one tool group is required")
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "chatops-mcp", Version: version.String()}, nil)
	seen := map[string]bool{}
	for _, name := range names {
		register, ok := groups[name]
		if !ok {
			return nil, fmt.Errorf("unknown tool group %q (available: %s)", name, availableGroups())
		}
		if seen[name] {
			return nil, fmt.Errorf("tool group %q listed more than once", name)
		}
		seen[name] = true
		register(server)
	}
	return server, nil
}

func availableGroups() string {
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}
