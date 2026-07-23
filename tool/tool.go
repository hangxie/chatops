// Package tool provides a generic interface for invoking operational
// tools such as kubernetes, proxmox, harbor, or the dummy ping tool.
//
// Each tool performs a single intent and lives in its own sub-package,
// exporting its URL scheme, opener, and descriptor; callers wire the
// tools they support into a Registry, so a tool instance can be opened
// from a single URL:
//
//	reg := tool.NewRegistry(
//		tool.Backend{Scheme: ping.Scheme, Opener: ping.Opener, Descriptor: &ping.Descriptor},
//	)
//	tl, err := reg.Open(ctx, "ping://", creds)
//	result, err := tl.Invoke(ctx, tool.Call{})
//
// A tool does one thing, so a Call carries no verb: the tool is the
// intent. This mirrors the Model Context Protocol, where each tool has
// a name and a flat input schema and a call supplies only arguments.
//
// The URL scheme selects the tool implementation, the host/port/path
// locate the endpoint the tool operates on (e.g. the kubernetes API
// server or the proxmox host), and query parameters carry any further
// instance configuration.
//
// Credential values are never part of the URL; tools resolve predefined
// cred.Key identifiers from the cred.Store passed to Open.
package tool

import (
	"context"
)

// Choice is one response a tool call offers to the human receiving its result.
// Chat backends may render choices as interactive controls and otherwise retain
// the message text as a plain-text fallback.
type Choice struct {
	Label string
	Value string
}

// Call describes one invocation of a tool. Because each tool performs a
// single intent, a Call names no verb: it carries only the arguments the
// tool reads.
type Call struct {
	// Arguments is the flat key-value bag the tool reads, keyed by the
	// parameter names in its Descriptor (e.g. "service": "github"). It
	// matches the tool's model-facing input schema. It may be nil.
	Arguments map[string]string

	// Choices optionally presents a bounded set of responses to the human.
	// It is internal planner-to-tool data (currently only the reply tool
	// uses it), never part of a tool's model-facing schema.
	Choices []Choice
}

// Result is the outcome of a successfully invoked Call.
type Result struct {
	// Text is the human-readable outcome, composed by the tool and
	// suitable for posting to chat as-is. It is the complete answer
	// for a human; callers never need Details to render a reply. It
	// is empty only when the tool has already delivered the outcome
	// to the human itself (for example the reply tool, whose intent
	// is posting into chat), so callers relay non-empty Text and
	// stay silent on empty Text.
	Text string

	// Details carries optional machine-readable key-value output
	// supplementing Text, for callers that act on the result rather
	// than display it. It may be nil.
	Details map[string]string
}

// Tool is an opened tool instance performing a single intent.
//
// Implementations must be safe for concurrent use by multiple
// goroutines, except that Close must not be called concurrently with
// Invoke.
type Tool interface {
	// Invoke performs the tool's operation with the arguments in call and
	// returns its outcome. It returns an error when the arguments are
	// invalid or the operation fails.
	Invoke(ctx context.Context, call Call) (Result, error)

	// Close releases any resources held by the tool. Calling Invoke
	// after Close is invalid.
	Close() error
}
