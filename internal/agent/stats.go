package agent

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/hangxie/chatops/internal/llm"
)

// Phase names a turn step by what it did.
type Phase string

const (
	// PhasePlan is a model call that returned tool calls.
	PhasePlan Phase = "plan"
	// PhaseAnswer is a model call that returned the final text.
	PhaseAnswer Phase = "answer"
	// PhaseModel is a model call that failed before returning either.
	PhaseModel Phase = "model"
	// PhaseTool is one MCP tool call.
	PhaseTool Phase = "tool"
)

// Outcome is how a step or a turn ended.
type Outcome string

const (
	OutcomeOK       Outcome = "ok"
	OutcomeAnswered Outcome = "answered"
	OutcomeStopped  Outcome = "stopped"
	OutcomeTimeout  Outcome = "timeout"
	OutcomeError    Outcome = "error"
)

// Step is one model or tool call of a turn.
type Step struct {
	Phase    Phase
	Outcome  Outcome
	Duration time.Duration
	// Usage is the model step's token count; nil when unreported.
	Usage *llm.Usage
	// Model names the model that answered, or failed last; Attempts has each model tried, with its own duration.
	Model    string
	Attempts []llm.Attempt
	// Name is the tool's model-facing name; Server and Tool are empty when the name is unknown.
	Name   string
	Server string
	Tool   string
	// ResultBytes is what the tool returned; SentBytes is what the model saw after truncation.
	ResultBytes int
	SentBytes   int
	// Truncated is set when the result exceeded max_tool_result_bytes.
	Truncated bool
}

// Stats records where a turn spent its time, tokens, and bytes.
type Stats struct {
	// Outcome is answered, stopped, timeout, or error.
	Outcome  Outcome
	Duration time.Duration
	Steps    []Step
}

// ToolCalls counts the tool steps.
func (s Stats) ToolCalls() int {
	n := 0
	for _, step := range s.Steps {
		if step.Phase == PhaseTool {
			n++
		}
	}
	return n
}

// Models lists the models that answered or failed last, each once, in first-use order.
func (s Stats) Models() []string {
	var models []string
	for _, step := range s.Steps {
		if step.Model != "" && !slices.Contains(models, step.Model) {
			models = append(models, step.Model)
		}
	}
	return models
}

// Tokens sums reported usage; reported is false when no step reported any.
func (s Stats) Tokens() (prompt, completion int, reported bool) {
	for _, step := range s.Steps {
		if step.Usage != nil {
			prompt += step.Usage.PromptTokens
			completion += step.Usage.CompletionTokens
			reported = true
		}
	}
	return prompt, completion, reported
}

func modelStep(resp llm.Response, err error, duration time.Duration) Step {
	step := Step{Phase: PhasePlan, Outcome: OutcomeOK, Duration: duration, Usage: resp.Usage, Model: resp.Model, Attempts: resp.Attempts}
	switch {
	case err != nil:
		step.Phase, step.Outcome = PhaseModel, ErrorOutcome(err)
	case len(resp.Message.ToolCalls) == 0:
		step.Phase = PhaseAnswer
	}
	return step
}

// ErrorOutcome is timeout for an error caused by a deadline, and error otherwise.
func ErrorOutcome(err error) Outcome {
	if errors.Is(err, context.DeadlineExceeded) {
		return OutcomeTimeout
	}
	return OutcomeError
}

func turnOutcome(steps []Step, err error) Outcome {
	if err != nil {
		return ErrorOutcome(err)
	}
	if len(steps) > 0 && steps[len(steps)-1].Outcome == OutcomeStopped {
		return OutcomeStopped
	}
	return OutcomeAnswered
}
