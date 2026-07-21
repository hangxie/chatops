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
// "anthropic://?model=claude-fable-5"). The tools argument is the set
// of operational tools the caller has enabled, passed through to the
// backend so an LLM-backed planner can offer them to the model (see
// Registry.Open); a nil set is treated as empty.
//
// Credential values are never part of the URL; backends resolve them
// (e.g. API keys) from the cred.Store passed to Open, under
// conventional key names prefixed by the backend name (for example
// openai-api-key, anthropic-api-key), overridable per instance with
// the cred-prefix query parameter.
//
// A plan is a sequence of tool invocations, each naming the tool by
// the URL it is opened from (see the tool package). Saying something
// back to the requester is itself a tool step: the tool/reply tool
// posts text into the conversation the message came from, so a
// clarifying question and an operational action have the same shape —
// mirroring how LLM tool-use APIs treat text output and tool calls as
// peers in one turn.
package planner

import (
	"context"

	"github.com/hangxie/chatops/tool"
)

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
	// Tool is the URL of the tool to invoke, e.g. "ping://" or
	// "reply://". The caller resolves it to an opened tool.Tool
	// (typically via a tool.Registry, or directly for tools like
	// tool/reply that are bound to the chat connection). Resolution
	// happens in the context of the request that produced the plan:
	// in particular "reply://" resolves to the reply tool bound to
	// the chat connection that request arrived on, which is what
	// keeps replies on the right connection when conversation IDs
	// collide across connections — steps carry no connection
	// identity of their own.
	Tool string

	// Call is the invocation to perform on the opened tool.
	Call tool.Call
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
