// Package agent runs one bounded turn: model calls and tool calls alternate until a plain-text answer.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/hangxie/chatops/internal/config"
	"github.com/hangxie/chatops/internal/llm"
	"github.com/hangxie/chatops/internal/mcp"
)

// maxRepeatedFailures is how often one exact call may fail in a row before the turn stops instead of running it again.
const maxRepeatedFailures = 2

// Model completes one model request.
type Model interface {
	Complete(ctx context.Context, req llm.Request) (llm.Response, error)
}

// Tools exposes the MCP tool catalog, executes calls, and reports which servers are down.
type Tools interface {
	Catalog() *mcp.Catalog
	Call(ctx context.Context, tool mcp.Tool, args json.RawMessage) (mcp.Result, error)
	Health() []mcp.ServerHealth
}

// Agent runs conversation turns against a model and a tool set.
type Agent struct {
	model  Model
	tools  Tools
	limits config.Agent
	logger *slog.Logger
}

// Turn is the outcome of one user message.
type Turn struct {
	// Reply is the text to post back to the user.
	Reply string
	// Messages are this turn's cross-turn history: the user message only. Assistant answers,
	// tool calls, and tool results are never replayed, so an earlier reply cannot bias later turns.
	Messages []llm.Message
	// Stats is filled in on failure too, with the steps that ran.
	Stats Stats
}

// New builds an agent; a nil logger uses slog.Default, and config validation ensures a prompt and a decline reply.
func New(model Model, tools Tools, limits config.Agent, logger *slog.Logger) *Agent {
	if logger == nil {
		logger = slog.Default()
	}
	return &Agent{model: model, tools: tools, limits: limits, logger: logger}
}

// Run answers input; model failures and an ended turn are errors, tool failures go back to the model.
// A failed turn returns only its Stats alongside the error.
func (a *Agent) Run(ctx context.Context, history []llm.Message, input string) (Turn, error) {
	start := time.Now()
	turn, err := a.run(ctx, history, input)
	turn.Stats.Duration = time.Since(start)
	turn.Stats.Outcome = turnOutcome(turn.Stats.Steps, err)
	a.logStats(turn.Stats)
	if err != nil {
		return Turn{Stats: turn.Stats}, err
	}
	return turn, nil
}

func (a *Agent) run(ctx context.Context, history []llm.Message, input string) (turn Turn, err error) {
	timeout := a.limits.TurnTimeout
	ctx, cancel := context.WithTimeoutCause(ctx, timeout, fmt.Errorf("turn exceeded %s: %w", timeout, context.DeadlineExceeded))
	defer cancel()

	userMessage := llm.Message{Role: llm.RoleUser, Content: input}
	// One snapshot per turn: tool-list changes apply from the next turn.
	catalog := a.tools.Catalog()
	if len(catalog.Tools()) == 0 {
		turn.Reply, turn.Messages = noToolsReply, []llm.Message{userMessage}
		return turn, nil
	}
	specs := append(toolSpecs(catalog), declineSpec)

	messages := make([]llm.Message, 0, len(history)+2)
	messages = append(messages, llm.Message{Role: llm.RoleSystem, Content: a.limits.SystemPrompt})
	messages = append(messages, history...)
	messages = append(messages, userMessage)
	steps := &turn.Stats.Steps
	failures := newFailureLog()

	for iteration := 1; ; iteration++ {
		req := llm.Request{Messages: messages, Tools: specs}
		if iteration == 1 && a.limits.RequireToolCall {
			req.ToolChoice = llm.ToolChoiceRequired
		}
		start := time.Now()
		resp, err := a.model.Complete(ctx, req)
		step := modelStep(resp, err, time.Since(start))
		// Dependencies may ignore ctx, so a late success must not count as an answer.
		if ctx.Err() != nil {
			step.Outcome = errorOutcome(context.Cause(ctx))
		}
		*steps = append(*steps, step)
		if ctx.Err() != nil {
			return turn, context.Cause(ctx)
		}
		if err != nil {
			return turn, fmt.Errorf("model: %w", err)
		}
		if len(resp.Message.ToolCalls) == 0 {
			if req.ToolChoice == llm.ToolChoiceRequired {
				(*steps)[len(*steps)-1].Outcome = OutcomeError
				return turn, fmt.Errorf("model: %w", errNoToolCall)
			}
			turn.Reply, turn.Messages = a.answerReply(resp.Message.Content, failures), []llm.Message{userMessage}
			return turn, nil
		}
		toolCalls, declined := splitDecline(resp.Message.ToolCalls)
		if len(toolCalls) == 0 {
			*steps = append(*steps, declineStep())
			turn.Reply, turn.Messages = a.declineReply(failures), []llm.Message{userMessage}
			return turn, nil
		}
		if declined {
			// Dropped from the transcript too, since every call sent back to the model needs a result.
			a.logger.Warn("decline dropped because the model also called other tools")
			resp.Message.ToolCalls = toolCalls
		}
		if iteration >= a.limits.MaxIterations {
			(*steps)[len(*steps)-1].Outcome = OutcomeStopped
			turn.Reply = withFailures(fmt.Sprintf("I stopped after %d steps without reaching an answer. Try a narrower request.", iteration), failures.unresolved())
			turn.Messages = []llm.Message{userMessage}
			a.logger.Warn("agent iteration limit reached", "iterations", iteration)
			return turn, nil
		}
		if failures.repeats(resp.Message.ToolCalls) {
			(*steps)[len(*steps)-1].Outcome = OutcomeStopped
			turn.Reply = withFailures("I stopped because the same tool call kept failing.", failures.unresolved())
			turn.Messages = []llm.Message{userMessage}
			a.logger.Warn("agent stopped on a repeated failing tool call", "iterations", iteration)
			return turn, nil
		}

		messages = append(messages, resp.Message)
		results, err := a.runCalls(ctx, catalog, resp.Message.ToolCalls, failures, steps)
		if err != nil {
			return turn, err
		}
		messages = append(messages, results...)
	}
}

// runCalls runs calls in order, recording each step and failure, and returns one tool message per call.
func (a *Agent) runCalls(ctx context.Context, catalog *mcp.Catalog, calls []llm.ToolCall, failures *failureLog, steps *[]Step) ([]llm.Message, error) {
	results := make([]llm.Message, 0, len(calls))
	for _, call := range calls {
		content, step, failure := a.runTool(ctx, catalog, call)
		*steps = append(*steps, step)
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		if step.Outcome == OutcomeOK {
			failures.succeed(call)
		} else {
			failures.fail(call, failureLine(step, failure))
		}
		results = append(results, llm.Message{Role: llm.RoleTool, ToolCallID: call.ID, Content: content})
	}
	return results, nil
}

func (a *Agent) logStats(stats Stats) {
	attrs := []any{"outcome", stats.Outcome, "duration", stats.Duration, "steps", len(stats.Steps), "tools", stats.ToolCalls()}
	// Unreported usage is left out rather than logged as zero.
	if prompt, completion, reported := stats.Tokens(); reported {
		attrs = append(attrs, "prompt_tokens", prompt, "completion_tokens", completion)
	}
	a.logger.Info("turn completed", attrs...)
}

func toolSpecs(catalog *mcp.Catalog) []llm.ToolSpec {
	tools := catalog.Tools()
	specs := make([]llm.ToolSpec, 0, len(tools))
	for _, tool := range tools {
		specs = append(specs, llm.ToolSpec{Name: tool.Name, Description: tool.Description, Parameters: modelSchema(tool.InputSchema)})
	}
	return specs
}
