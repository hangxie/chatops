// Package serve implements `chatops-mcp serve`: selected tool groups over stdio or Streamable HTTP.
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
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes"
	"github.com/hangxie/chatops/pkg/mcpserver/ping"
	"github.com/hangxie/chatops/pkg/mcpserver/status"
)

// groups maps tool group names accepted by --tool to their registration, given the --config directory.
var groups = map[string]func(*mcp.Server, string) error{
	"ping":       registerPing,
	"status":     registerStatus,
	"kubernetes": registerKubernetes,
}

// Cmd is the kong command for serve.
type Cmd struct {
	Tool     string `help:"Tool group to serve; one group per process (available: kubernetes, ping, status)." default:"ping"`
	HTTP     string `help:"Serve Streamable HTTP at /mcp on this address (e.g. 127.0.0.1:8080) instead of stdio." placeholder:"ADDR"`
	TokenEnv string `help:"Environment variable holding the bearer token HTTP clients must send; required when --http is not a loopback address." placeholder:"NAME"`
	Config   string `help:"Config directory of the tool group: only its *.yaml files are loaded, merged in file name order. Required by status, optional for kubernetes, not taken by ping." placeholder:"DIR" type:"path"`

	// transport overrides stdio and listener overrides --http listening in tests.
	transport mcp.Transport
	listener  net.Listener
}

// Run serves MCP until the client disconnects or ctx is cancelled.
func (c Cmd) Run(ctx context.Context) error {
	server, err := newServer(c)
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

func newServer(c Cmd) (*mcp.Server, error) {
	register, ok := groups[c.Tool]
	if !ok {
		return nil, fmt.Errorf("unknown tool group %q (available: %s)", c.Tool, availableGroups())
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "chatops-mcp", Version: version.String()}, nil)
	if err := register(server, c.Config); err != nil {
		return nil, fmt.Errorf("tool group %q: %w", c.Tool, err)
	}
	return server, nil
}

func registerPing(server *mcp.Server, dir string) error {
	if dir != "" {
		return errors.New("--config is not supported")
	}
	ping.Register(server)
	return nil
}

func registerStatus(server *mcp.Server, dir string) error {
	if dir == "" {
		return errors.New("--config is required")
	}
	cfg, err := status.Load(dir)
	if err != nil {
		return err
	}
	return status.Register(server, cfg)
}

func registerKubernetes(server *mcp.Server, dir string) error {
	var cfg kubernetes.Config
	if dir != "" {
		var err error
		if cfg, err = kubernetes.Load(dir); err != nil {
			return err
		}
	}
	return kubernetes.Register(server, cfg)
}

func availableGroups() string {
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}
