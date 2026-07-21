package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/hangxie/chatops/chat"
	"github.com/hangxie/chatops/planner"
	"github.com/hangxie/chatops/tool"
	"github.com/hangxie/chatops/tool/reply"
)

// handle plans one inbound message and executes every step of the plan,
// logging the planner decision and each tool invocation so operators can see
// how a message flowed through the planner and the tools.
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
		log.Error("planner failed", "error", err.Error())
		return fmt.Errorf("engine: plan message: %w", err)
	}
	log.Info("plan produced", "steps", len(plan.Steps), "tools", stepTools(plan.Steps))

	for i, step := range plan.Steps {
		stepLog := log.With("step", i+1, "tool", step.Tool, "action", step.Call.Action, "target", step.Call.Target)
		stepLog.Info("executing step")
		result, invokeErr := e.invoke(ctx, stepLog, msg.ConversationID, step)
		if invokeErr != nil {
			stepLog.Error("step failed", "error", invokeErr.Error())
			return fmt.Errorf("engine: execute step %d (%q): %w", i+1, step.Tool, invokeErr)
		}
		if result.Text == "" {
			stepLog.Debug("step produced no output")
			continue
		}
		if sendErr := e.chat.Send(ctx, chat.Message{ConversationID: msg.ConversationID, Text: result.Text}); sendErr != nil {
			stepLog.Error("posting result failed", "error", sendErr.Error())
			return fmt.Errorf("engine: send result for step %d (%q): %w", i+1, step.Tool, sendErr)
		}
		stepLog.Info("result posted")
	}
	return nil
}

// stepTools lists the tool URLs a plan invokes, for a compact log summary of
// what the planner decided to do.
func stepTools(steps []planner.Step) []string {
	tools := make([]string, len(steps))
	for i, step := range steps {
		tools[i] = step.Tool
	}
	return tools
}

func (e *Engine) invoke(ctx context.Context, log *slog.Logger, conversationID string, step planner.Step) (result tool.Result, err error) {
	u, err := url.Parse(step.Tool)
	if err != nil {
		return tool.Result{}, fmt.Errorf("parse tool URL: %w", err)
	}
	if strings.EqualFold(u.Scheme, reply.Scheme) {
		if !strings.EqualFold(step.Tool, reply.URL) {
			return tool.Result{}, fmt.Errorf("reply: URL %q takes no endpoint or configuration", step.Tool)
		}
		step.Call.Target = conversationID
		log.Debug("posting reply to conversation")
		return e.reply.Invoke(ctx, step.Call)
	}

	// Tool instances are deliberately scoped to one step for isolated ownership
	// and cleanup. Expensive backends can pool resources behind their opener.
	log.Debug("opening tool")
	opened, err := e.tools.Open(ctx, step.Tool, e.credentials)
	if err != nil {
		return tool.Result{}, fmt.Errorf("open tool: %w", err)
	}
	defer func() {
		if closeErr := opened.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close tool: %w", closeErr))
		}
	}()
	result, err = opened.Invoke(ctx, step.Call)
	if err != nil {
		return tool.Result{}, fmt.Errorf("invoke tool: %w", err)
	}
	log.Debug("tool invoked", "has_output", result.Text != "")
	return result, nil
}
