package engine

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hangxie/chatops/chat"
	"github.com/hangxie/chatops/mcphost"
	"github.com/hangxie/chatops/planner"
)

// failureNotice is posted to the requester when handling their message fails
// for a non-fatal reason (a bad plan, or a tool that could not be called).
// The specific error is logged, not shown, so internal detail does not leak
// into chat.
const failureNotice = "sorry, I couldn't complete that request"

// handle plans one inbound message and executes every step of the plan,
// logging the planner decision and each tool call so operators can see how a
// message flowed through the planner and the tools. Errors are returned
// wrapped; processMessage decides whether they are fatal and logs them.
func (e *Engine) handle(ctx context.Context, msg chat.Message) error {
	log := e.logger.With("conversation_id", msg.ConversationID, "sender", msg.Sender)
	log.Info("message received")
	log.Debug("planning message", "text", msg.Text)

	plan, err := e.planner.Plan(ctx, planner.Request{
		Text:           msg.Text,
		ConnectionID:   e.connectionID,
		ConversationID: msg.ConversationID,
		Sender:         msg.Sender,
	})
	if err != nil {
		return fmt.Errorf("engine: plan message: %w", err)
	}
	log.Info("plan produced", "steps", len(plan.Steps), "tools", stepTools(plan.Steps))

	// Tools that act on the requester's conversation — the reply tool, and
	// anything asking the requester a question — read it from the context
	// rather than from arguments the model chose, so a reply cannot be
	// misrouted by a bad plan.
	ctx = chat.WithConversation(ctx, msg.ConversationID)

	for i, step := range plan.Steps {
		stepLog := log.With("step", i+1, "tool", step.Tool)
		stepLog.Info("executing step")
		text, failed, invokeErr := e.invoke(ctx, stepLog, step)
		if invokeErr != nil {
			return fmt.Errorf("engine: execute step %d (%q): %w", i+1, step.Tool, invokeErr)
		}
		if text == "" {
			stepLog.Debug("step produced no output")
		} else if sendErr := e.chat.Send(ctx, chat.Message{ConversationID: msg.ConversationID, Text: text}); sendErr != nil {
			return fmt.Errorf("engine: send result for step %d (%q): %w", i+1, step.Tool, sendErr)
		} else {
			stepLog.Info("result posted")
		}
		if failed {
			// A later step may well have assumed this one worked, so the rest
			// of the plan is abandoned rather than run against a state that
			// did not come about. The requester has already been told what
			// the tool said.
			stepLog.Warn("plan abandoned after a failed step", "remaining", len(plan.Steps)-i-1)
			return nil
		}
	}
	return nil
}

// invoke calls one tool through the host and renders its result for chat. It
// reports the text to post and whether the tool reported a failure.
//
// A tool reporting its own failure (MCP's IsError) is not an engine error:
// the tool ran and said what went wrong, so its message is relayed to the
// requester like any other output. Only a call that could not be made at all
// — an unknown tool, an unreachable server — fails the step.
//
// A failure with nothing to say falls back to the generic notice. The SDK
// always fills in content when a handler errors, so a built-in cannot produce
// one, but an external server may send isError with no content at all — and
// the requester must not be left in silence.
func (e *Engine) invoke(ctx context.Context, log *slog.Logger, step planner.Step) (text string, failed bool, err error) {
	result, err := e.tools.CallTool(ctx, step.Tool, step.Arguments)
	if err != nil {
		return "", false, err
	}
	text = mcphost.Render(result)
	if isToolError(result) {
		log.Warn("tool reported an error", "output", text)
		if text == "" {
			text = failureNotice
		}
		return text, true, nil
	}
	log.Debug("tool called", "has_output", text != "")
	return text, false, nil
}

// isToolError reports whether a result carries a tool-reported failure.
func isToolError(result *mcp.CallToolResult) bool {
	return result != nil && result.IsError
}

// notifyFailure posts the generic failure notice to conversationID so the
// requester knows their message could not be completed. Any error delivering
// the notice is logged and otherwise ignored — the engine keeps running.
func (e *Engine) notifyFailure(ctx context.Context, conversationID string) {
	if err := e.chat.Send(ctx, chat.Message{ConversationID: conversationID, Text: failureNotice}); err != nil {
		e.logger.Error("failure notice not delivered", "conversation_id", conversationID, "error", err.Error())
	}
}

// stepTools lists the tools a plan invokes, for a compact log summary of what
// the planner decided to do.
func stepTools(steps []planner.Step) []string {
	tools := make([]string, len(steps))
	for i, step := range steps {
		tools[i] = step.Tool
	}
	return tools
}
