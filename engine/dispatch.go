package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"strings"

	"github.com/hangxie/chatops/chat"
	"github.com/hangxie/chatops/planner"
	"github.com/hangxie/chatops/tool"
	"github.com/hangxie/chatops/tool/reply"
)

// failureNotice is posted to the requester when handling their message fails
// for a non-fatal reason (a bad plan or a tool error). The specific error is
// logged, not shown, so internal detail does not leak into chat.
const failureNotice = "sorry, I couldn't complete that request"

// handle runs one inbound message through the agentic loop: plan, execute the
// steps, feed feedback-tool results back, and re-plan until a round produces no
// feedback step or the round/byte cap forces a final summarizing plan. Reply
// steps and fire-and-post tool results are posted to chat as they occur. Errors
// are returned wrapped; processMessage decides whether they are fatal and logs
// them. The planner is told the turn ended on every exit so it can drop the
// in-flight transcript.
func (e *Engine) handle(ctx context.Context, msg chat.Message) error {
	log := e.logger.With("conversation_id", msg.ConversationID, "sender", msg.Sender)
	log.Info("message received")
	log.Debug("planning message", "text", msg.Text)

	defer e.endTurn(msg.ConversationID)

	req := planner.Request{
		Text:           msg.Text,
		ConnectionID:   e.connectionID,
		ConversationID: msg.ConversationID,
		Sender:         msg.Sender,
	}
	executionRounds := 0
	turnBytes := 0

	for {
		plan, err := e.planner.Plan(ctx, req)
		if err != nil {
			return fmt.Errorf("engine: plan message: %w", err)
		}
		log.Info("plan produced", "steps", len(plan.Steps), "tools", stepTools(plan.Steps), "final", req.Final)

		outcome, err := e.runPlan(ctx, log, msg.ConversationID, plan)
		if err != nil {
			return err
		}
		// The turn ends when a fire-and-post UserError was already surfaced,
		// when this was the forced final round, or when the round produced no
		// feedback step (a final answer, a question, or fire-and-post only).
		if outcome.surfaced || req.Final || len(outcome.feedback) == 0 {
			return nil
		}

		executionRounds++
		for _, r := range outcome.feedback {
			turnBytes += len(r.Content)
		}
		req = planner.Request{
			ConnectionID:   e.connectionID,
			ConversationID: msg.ConversationID,
			Results:        outcome.feedback,
			Final:          executionRounds >= e.maxToolRounds || turnBytes > e.maxTurnBytes,
		}
	}
}

// planOutcome is the result of executing one plan's steps.
type planOutcome struct {
	// feedback holds the results of feedback tool steps, to feed to the next
	// planning round. It is empty when the round had no feedback step.
	feedback []planner.StepResult
	// surfaced reports that a fire-and-post tool returned a UserError whose
	// safe message was posted to chat, ending the turn.
	surfaced bool
}

// runPlan executes each step of plan in order. Reply steps and fire-and-post
// tool results are posted to chat; feedback tool results are collected to feed
// back. A feedback step's UserError is fed back so the model can recover; any
// other tool error is fatal — a fire-and-post UserError posts its safe message
// and ends the turn, and everything else is returned for the generic notice.
func (e *Engine) runPlan(ctx context.Context, log *slog.Logger, conversationID string, plan planner.Plan) (planOutcome, error) {
	var outcome planOutcome
	for i, step := range plan.Steps {
		stepLog := log.With("step", i+1, "tool", step.Tool)
		stepLog.Info("executing step")
		result, err := e.invoke(ctx, stepLog, conversationID, step)
		if err != nil {
			if step.Feedback {
				if msg, ok := tool.UserMessage(err); ok {
					stepLog.Info("tool error fed back for retry")
					outcome.feedback = append(outcome.feedback, planner.StepResult{
						ID: step.ID, Content: boundContent(msg, e.maxResultBytes), IsError: true,
					})
					continue
				}
				return outcome, fmt.Errorf("engine: execute step %d (%q): %w", i+1, step.Tool, err)
			}
			if msg, ok := tool.UserMessage(err); ok {
				if sendErr := e.chat.Send(ctx, chat.Message{ConversationID: conversationID, Text: msg}); sendErr != nil {
					return outcome, fmt.Errorf("engine: send error for step %d (%q): %w", i+1, step.Tool, sendErr)
				}
				stepLog.Info("tool error posted")
				outcome.surfaced = true
				return outcome, nil
			}
			return outcome, fmt.Errorf("engine: execute step %d (%q): %w", i+1, step.Tool, err)
		}
		if step.Feedback {
			outcome.feedback = append(outcome.feedback, planner.StepResult{
				ID: step.ID, Content: boundContent(canonicalContent(result), e.maxResultBytes),
			})
			stepLog.Debug("tool result fed back")
			continue
		}
		if result.Text == "" {
			stepLog.Debug("step produced no output")
			continue
		}
		if sendErr := e.chat.Send(ctx, chat.Message{ConversationID: conversationID, Text: result.Text}); sendErr != nil {
			return outcome, fmt.Errorf("engine: send result for step %d (%q): %w", i+1, step.Tool, sendErr)
		}
		stepLog.Info("result posted")
	}
	return outcome, nil
}

// notifyFailure posts the generic failure notice to conversationID so the
// requester knows their message could not be completed. Any error delivering
// the notice is logged and otherwise ignored — the engine keeps running.
func (e *Engine) notifyFailure(ctx context.Context, conversationID string) {
	if err := e.chat.Send(ctx, chat.Message{ConversationID: conversationID, Text: failureNotice}); err != nil {
		e.logger.Error("failure notice not delivered", "conversation_id", conversationID, "error", err.Error())
	}
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
		// Inject the conversation the request arrived on; the planner leaves
		// it unset so replies land on the right connection. Copy the
		// arguments rather than mutating the planner's map, which a
		// concurrent-safe planner may share across conversations.
		args := make(map[string]string, len(step.Call.Arguments)+1)
		maps.Copy(args, step.Call.Arguments)
		args["conversation"] = conversationID
		step.Call.Arguments = args
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
