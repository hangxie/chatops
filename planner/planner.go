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
// Credential values are never part of the URL; the selected backend resolves
// the single predefined planner API key from the cred.Store passed to Open.
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

// Request is one inbound chat message for the planner to act on, or a
// continuation of an in-flight turn carrying the previous round's tool results.
// Exactly one shape is valid per call:
//
//   - human turn:   Text set, Results nil, Final false
//   - continuation: Text empty (ignored), Results set, Final false
//   - final round:  Final true (Results may be set) — the engine is out of tool
//     budget and needs a terminal answer, so the planner must offer the model no
//     tools and produce a reply-only plan.
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

	// Results carries the outcomes of the previous round's feedback tool
	// steps, correlated to their Step.ID, on a continuation of an in-flight
	// turn. It is nil on the first (human) round.
	Results []StepResult

	// Final marks the engine's forced summarizing round: it is out of tool
	// budget (the round or byte cap), so the planner must offer the model no
	// tools and return a reply-only plan.
	Final bool
}

// StepResult is one executed feedback tool step's outcome, fed back to the
// planner on the next round.
type StepResult struct {
	// ID matches the Step.ID that produced this result.
	ID string

	// Content is the engine's canonical, already-bounded rendering of the
	// tool Result (or of a tool.UserError message when IsError), suitable to
	// place verbatim into a provider tool-result message.
	Content string

	// IsError reports that the tool returned a tool.UserError; Content is its
	// safe message, fed back so the model can correct and retry.
	IsError bool
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

	// Feedback selects the step's disposition. When true, the engine feeds
	// the step's Result back to the planner for the next round (the agentic
	// path) instead of posting it to chat. When false (the zero value), the
	// engine posts Result.Text to chat directly — the fire-and-post path that
	// keeps fixed planners working unchanged. Disposition is explicit, never
	// inferred from whether ID is set.
	Feedback bool

	// ID correlates a result back to this step. It is required — non-empty
	// and unique within the plan — for every step the planner retains as a
	// provider tool call: feedback tool steps and reply steps that came from
	// a provider tool call. It is empty for fire-and-post steps and
	// assistant-prose replies, and is not tied to Feedback (a reply call has
	// Feedback false but still needs an ID).
	ID string
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

// TurnCloser is an optional interface a stateful planner implements so the
// engine can tell it a turn has ended — on a normal terminal round or an abort
// — and it can drop the in-flight transcript it kept for that turn. It takes no
// context on purpose: abort paths often carry an already-canceled context, and
// this cleanup must run regardless. Implementations must not block and must not
// make network calls. Fixed planners that keep no per-turn state need not
// implement it; the engine calls it only when present.
type TurnCloser interface {
	EndTurn(connectionID, conversationID string)
}
