package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/llm"
)

// withoutDurations zeroes timings so steps compare by value.
func withoutDurations(steps []Step) []Step {
	steps = slices.Clone(steps)
	for i := range steps {
		steps[i].Duration = 0
	}
	return steps
}

func Test_modelStep(t *testing.T) {
	usage := &llm.Usage{PromptTokens: 10, CompletionTokens: 2}
	attempts := []llm.Attempt{{Model: "a", Duration: 700 * time.Millisecond, Err: errors.New("503")}, {Model: "b", Duration: 300 * time.Millisecond}}
	tests := map[string]struct {
		resp llm.Response
		err  error
		want Step
	}{
		"plan": {
			resp: llm.Response{Message: llm.Message{ToolCalls: []llm.ToolCall{{Name: "x"}}}, Usage: usage},
			want: Step{Phase: PhasePlan, Outcome: OutcomeOK, Usage: usage, Duration: time.Second},
		},
		"answer": {
			resp: llm.Response{Message: llm.Message{Content: "hi"}},
			want: Step{Phase: PhaseAnswer, Outcome: OutcomeOK, Duration: time.Second},
		},
		"error": {
			err:  errors.New("503"),
			want: Step{Phase: PhaseModel, Outcome: OutcomeError, Duration: time.Second},
		},
		"timeout": {
			err:  fmt.Errorf("request: %w", context.DeadlineExceeded),
			want: Step{Phase: PhaseModel, Outcome: OutcomeTimeout, Duration: time.Second},
		},
		"failed-over": {
			resp: llm.Response{Message: llm.Message{Content: "hi"}, Model: "b", Attempts: attempts},
			want: Step{Phase: PhaseAnswer, Outcome: OutcomeOK, Duration: time.Second, Model: "b", Attempts: attempts},
		},
		"failed-after-failover": {
			resp: llm.Response{Model: "b", Attempts: attempts},
			err:  errors.New("503"),
			want: Step{Phase: PhaseModel, Outcome: OutcomeError, Duration: time.Second, Model: "b", Attempts: attempts},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, modelStep(tc.resp, tc.err, time.Second))
		})
	}
}

func Test_turnOutcome(t *testing.T) {
	tests := map[string]struct {
		steps []Step
		err   error
		want  Outcome
	}{
		"answered": {steps: []Step{{Phase: PhaseAnswer, Outcome: OutcomeOK}}, want: OutcomeAnswered},
		"stopped":  {steps: []Step{{Phase: PhasePlan, Outcome: OutcomeStopped}}, want: OutcomeStopped},
		"timeout":  {err: fmt.Errorf("turn: %w", context.DeadlineExceeded), want: OutcomeTimeout},
		"error":    {steps: []Step{{Phase: PhaseModel, Outcome: OutcomeError}}, err: errors.New("model: 503"), want: OutcomeError},
		"canceled": {err: context.Canceled, want: OutcomeError},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, turnOutcome(tc.steps, tc.err))
		})
	}
}

func Test_Stats_totals(t *testing.T) {
	tests := map[string]struct {
		steps      []Step
		tools      int
		prompt     int
		completion int
		reported   bool
	}{
		"empty": {},
		"mixed": {
			steps: []Step{
				{Phase: PhasePlan, Usage: &llm.Usage{PromptTokens: 100, CompletionTokens: 10}},
				{Phase: PhaseTool},
				{Phase: PhaseTool},
				{Phase: PhaseAnswer, Usage: &llm.Usage{PromptTokens: 300, CompletionTokens: 40}},
			},
			tools: 2, prompt: 400, completion: 50, reported: true,
		},
		"partly-unreported": {
			steps:  []Step{{Phase: PhasePlan}, {Phase: PhaseAnswer, Usage: &llm.Usage{PromptTokens: 5, CompletionTokens: 1}}},
			prompt: 5, completion: 1, reported: true,
		},
		"unreported": {steps: []Step{{Phase: PhaseAnswer}}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			stats := Stats{Steps: tc.steps}
			require.Equal(t, tc.tools, stats.ToolCalls())
			prompt, completion, reported := stats.Tokens()
			require.Equal(t, tc.prompt, prompt)
			require.Equal(t, tc.completion, completion)
			require.Equal(t, tc.reported, reported)
		})
	}
}
