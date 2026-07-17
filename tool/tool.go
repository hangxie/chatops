// Package tool provides a generic interface for invoking operational
// tools such as kubernetes, proxmox, harbor, or the dummy ping tool.
//
// Each tool lives in its own sub-package and exports its URL scheme
// and opener; callers wire the tools they support into a Registry, so
// a tool instance can be opened from a single URL:
//
//	reg := tool.NewRegistry(
//		tool.Backend{Scheme: ping.Scheme, Opener: ping.Opener},
//	)
//	tl, err := reg.Open(ctx, "ping://", creds)
//	result, err := tl.Invoke(ctx, tool.Call{Action: "ping"})
//
// The URL scheme selects the tool implementation, the host/port/path
// locate the endpoint the tool operates on (e.g. the kubernetes API
// server or the proxmox host), and query parameters carry any further
// instance configuration.
//
// Credential values are never part of the URL; tools resolve them
// from the cred.Store passed to Open. Each tool defines conventional
// key names prefixed by its name (for example k8s-ca, k8s-cert,
// k8s-key or harbor-user, harbor-password), and the prefix can be
// overridden per instance with the cred-prefix query parameter (e.g.
// "kubernetes://prod.example.com:6443?cred-prefix=k8s-prod" resolves
// k8s-prod-ca and so on) so multiple instances of the same tool can
// use distinct credentials.
package tool

import (
	"context"
	"errors"
)

// ErrUnknownAction is the sentinel error reported by Invoke when the
// call's Action is not one the tool supports. Tools wrap it with
// context, so check for it with errors.Is.
var ErrUnknownAction = errors.New("unknown action")

// Call describes one operation to perform. It carries enough detail
// for the tool to act but does not prescribe how the tool maps it to
// actual API calls or commands.
type Call struct {
	// Action is the verb to perform, in tool-specific vocabulary
	// (e.g. "restart", "scale", "ping").
	Action string

	// Target is what the action applies to, in tool-specific form
	// (e.g. a kubernetes "deployment/web"). It may be empty for
	// actions that have no target.
	Target string

	// Parameters carries optional key-value arguments for the action
	// (e.g. "replicas": "3"). It may be nil.
	Parameters map[string]string
}

// Result is the outcome of a successfully invoked Call.
type Result struct {
	// Text is the human-readable outcome, composed by the tool and
	// suitable for posting to chat as-is. It is the complete answer
	// for a human; callers never need Details to render a reply. It
	// is empty only when the tool has already delivered the outcome
	// to the human itself (for example the reply tool, whose action
	// is posting into chat), so callers relay non-empty Text and
	// stay silent on empty Text.
	Text string

	// Details carries optional machine-readable key-value output
	// supplementing Text, for callers that act on the result rather
	// than display it. It may be nil.
	Details map[string]string
}

// Tool is an opened tool instance.
//
// Implementations must be safe for concurrent use by multiple
// goroutines, except that Close must not be called concurrently with
// Invoke.
type Tool interface {
	// Invoke performs the operation described by call and returns its
	// outcome. It returns an error wrapping ErrUnknownAction when
	// call.Action is not one the tool supports.
	Invoke(ctx context.Context, call Call) (Result, error)

	// Close releases any resources held by the tool. Calling Invoke
	// after Close is invalid.
	Close() error
}
