// Package planner provides a generic interface for turning free-form
// chat messages into executable plans, backed by pluggable planner
// backends — LLM providers such as OpenAI and Anthropic, or the dummy
// ping planner.
//
// Each backend lives in its own sub-package and exports its URL
// scheme and opener; callers wire the backends they support into a
// Registry, so a planner can be opened from a single URL:
//
//	reg := planner.NewRegistry(
//		planner.Backend{Scheme: ping.Scheme, Opener: ping.Opener},
//	)
//	p, err := reg.Open(ctx, "ping://", creds, tools)
//	plan, err := p.Plan(ctx, planner.Request{
//		Text:           msg.Text,
//		ConversationID: msg.ConversationID,
//		Sender:         msg.Sender,
//	})
//
// The URL scheme selects the backend, the host/port/path locate the
// endpoint it talks to (empty for providers with a well-known API
// endpoint), and query parameters carry further configuration such as
// the model (e.g. "openai-chat-completions://api.openai.com/v1?model=gpt-5",
// "anthropic://?model=claude-fable-5"). The tools argument is the live
// catalog of Model Context Protocol tools the caller has enabled, passed
// through to the backend so an LLM-backed planner can offer them to the
// model (see Registry.Open); a nil source is treated as empty.
//
// Credential values are never part of the URL; the selected backend resolves
// the single predefined planner API key from the cred.Store passed to Open.
//
// A plan is a sequence of tool invocations, each naming a tool from
// the catalog the planner was opened with. Saying something back to
// the requester is itself a tool step: the reply tool posts text into
// the conversation the message came from, so a clarifying question and
// an operational action have the same shape — mirroring how LLM
// tool-use APIs treat text output and tool calls as peers in one turn.
package planner

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolSource is the live catalog of tools a planner may offer the model.
//
// It is live rather than a snapshot: a server may report a changed tool list
// at any time, so a planner rebuilds its offer from the source instead of
// freezing it when it was opened. Generation changes whenever the catalog
// does, so a planner can cache what it derives from the catalog and rebuild
// only when that value moves.
//
// Listing cannot fail. A source that cannot reach a server drops that
// server's tools and says so through its own logs; failing the requester's
// message because one of several servers is unwell would be the wrong
// response, so the catalog is always readable and may simply be smaller.
type ToolSource interface {
	Tools(ctx context.Context) []*mcp.Tool
	Generation() uint64
}

// Request is one inbound chat message for the planner to act on.
type Request struct {
	// Text is the free-form message from the human.
	Text string

	// ConnectionID identifies the chat connection the message arrived
	// on. Conversation IDs are only unique within one chat.Conn, so a
	// caller serving several connections from one planner must assign
	// each connection a distinct opaque ID to keep their
	// conversations' planner state apart; a caller with a single
	// connection (or one planner per connection) may leave it empty.
	ConnectionID string

	// ConversationID identifies the topic or thread the message
	// belongs to, as computed by the chat backend
	// (chat.Message.ConversationID), scoped to the connection
	// identified by ConnectionID. Planners use it to keep
	// per-conversation context across requests, and it is the reply
	// target for steps that post back to the requester.
	ConversationID string

	// Sender identifies who sent the message, in chat-backend-native
	// form (chat.Message.Sender). It may be empty for backends without
	// a notion of identity.
	Sender string
}

// Step is one tool invocation of a plan.
type Step struct {
	// Tool names the tool to invoke, as it appears in the catalog the
	// planner was opened with (e.g. "ping" or "reply"). The caller
	// dispatches it in the context of the request that produced the plan:
	// in particular the reply tool posts into the conversation that
	// request arrived on, which is what keeps replies on the right
	// connection when conversation IDs collide across connections —
	// steps carry no connection identity of their own.
	Tool string

	// Arguments is the tool's input, matching its model-facing schema.
	// It may be nil for a tool that reads nothing.
	Arguments map[string]any
}

// Plan is the planner's decision on one request: the steps to execute
// in order. It may be empty when the planner decides nothing needs to
// be done. A plan is not self-contained: the caller executes it in
// the context of the request that produced it (see Step.Tool).
type Plan struct {
	Steps []Step
}

// Planner is an opened planner backend.
//
// Implementations must be safe for concurrent use by multiple
// goroutines, except that Close must not be called concurrently with
// Plan.
type Planner interface {
	// Plan decides what to do about one inbound message and returns
	// the steps to execute. Asking the requester a clarifying question
	// is expressed as a step invoking the reply tool, not as an error;
	// Plan reports an error only when it cannot produce a decision
	// (e.g. the backend is unreachable).
	Plan(ctx context.Context, req Request) (Plan, error)

	// Close releases any resources held by the planner. Calling Plan
	// after Close is invalid.
	Close() error
}
