// Package mcp implements the command serving chatops' built-in tools to
// other Model Context Protocol hosts.
//
// The same server objects the engine connects to in process are served here
// over stdio instead, so a group reached this way behaves identically to one
// reached in process — that is what makes moving a group out of process a
// change of configuration rather than of code.
package mcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hangxie/chatops/cred"
	"github.com/hangxie/chatops/internal/registry"
	"github.com/hangxie/chatops/mcpserve"
)

// Cmd is the kong command for mcp.
type Cmd struct {
	Serve ServeCmd `cmd:"" help:"Serve a built-in tool group over stdio."`
}

// ServeCmd serves one built-in group on stdin/stdout.
type ServeCmd struct {
	Group          string `arg:"" help:"Built-in tool group to serve, as a bare name (k8s) or a name with options (k8s?context=prod)."`
	CredentialsURL string `name:"credentials" help:"Optional credential store URL for the served group."`
}

// Run serves the group until stdin closes or ctx is cancelled.
//
// Output on stdout is the protocol itself, so nothing else may be written
// there; diagnostics belong on stderr.
func (c *ServeCmd) Run(ctx context.Context) (err error) {
	name, query, err := mcpserve.ParseSpec(c.Group)
	if err != nil {
		return err
	}

	var credentials cred.Store
	if c.CredentialsURL != "" {
		credentials, err = registry.Credential().Open(ctx, c.CredentialsURL)
		if err != nil {
			return fmt.Errorf("mcp: open credentials: %w", err)
		}
		defer func() {
			if closeErr := credentials.Close(); closeErr != nil {
				err = errors.Join(err, fmt.Errorf("mcp: close credentials: %w", closeErr))
			}
		}()
	}

	srv, err := registry.Builtin().Server(name, mcpserve.Options{Query: query, Credentials: credentials})
	if err != nil {
		return err
	}
	// A host ends a stdio session by closing the pipe, and a termination
	// signal cancels the context; neither is a failure.
	if err := srv.Run(ctx, &mcp.StdioTransport{}); !mcpserve.GracefulStop(err) {
		return fmt.Errorf("mcp: serve %q: %w", name, err)
	}
	return nil
}
