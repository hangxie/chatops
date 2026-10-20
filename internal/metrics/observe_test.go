package metrics

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/agent"
	"github.com/hangxie/chatops/internal/conversation"
	"github.com/hangxie/chatops/internal/llm"
)

var _ conversation.Observer = (*Metrics)(nil)

var errModelTimeout = fmt.Errorf("model call exceeded 30s: %w", context.DeadlineExceeded)

func Test_Metrics_questions(t *testing.T) {
	m := New("v0")
	m.Received("slack")
	m.Received("slack")
	m.Received("unix")
	m.Rejected("slack", conversation.RejectBusy)
	m.Started("slack", 1500*time.Millisecond)
	m.Delivered("slack", agent.OutcomeAnswered, 3*time.Second)
	m.Delivered("slack", agent.OutcomeAnswered, 5*time.Second)
	m.Delivered("unix", agent.OutcomeTimeout, 120*time.Second)
	m.ReplyFailed("unix")

	requireLines(t, scrape(t, m),
		`chatops_questions_received_total{backend="slack"} 2`,
		`chatops_questions_received_total{backend="unix"} 1`,
		`chatops_questions_rejected_total{backend="slack",reason="busy"} 1`,
		`chatops_queue_wait_seconds_count{backend="slack"} 1`,
		`chatops_queue_wait_seconds_sum{backend="slack"} 1.5`,
		`chatops_response_duration_seconds_count{backend="slack",outcome="answered"} 2`,
		`chatops_response_duration_seconds_sum{backend="slack",outcome="answered"} 8`,
		`chatops_response_duration_seconds_count{backend="unix",outcome="timeout"} 1`,
		`chatops_reply_failures_total{backend="unix"} 1`,
	)
}

func Test_Metrics_Finished(t *testing.T) {
	m := New("v0")
	m.Finished("slack", agent.Stats{Outcome: agent.OutcomeAnswered, Steps: []agent.Step{
		{Phase: agent.PhasePlan, Outcome: agent.OutcomeOK, Duration: 2 * time.Second, Usage: &llm.Usage{PromptTokens: 1200, CompletionTokens: 45}, Model: "qwen"},
		{Phase: agent.PhaseTool, Outcome: agent.OutcomeOK, Duration: 500 * time.Millisecond, Name: "kubernetes__pod_list", Server: "kubernetes", Tool: "pod_list", ResultBytes: 126000, SentBytes: 65600, Truncated: true},
		{Phase: agent.PhaseTool, Outcome: agent.OutcomeError, Duration: time.Millisecond, Name: "kubernetes__pod_list", Server: "kubernetes", Tool: "pod_list", SentBytes: 40},
		{Phase: agent.PhaseTool, Outcome: agent.OutcomeError, Name: "made__up", SentBytes: 30},
		{Phase: agent.PhaseAnswer, Outcome: agent.OutcomeOK, Duration: 34 * time.Second, Model: "nemotron", Attempts: []llm.Attempt{
			{Model: "qwen", Duration: 30 * time.Second, Err: errModelTimeout},
			{Model: "nemotron", Duration: 4 * time.Second},
		}},
	}})
	m.Finished("slack", agent.Stats{Outcome: agent.OutcomeTimeout, Steps: []agent.Step{
		{Phase: agent.PhaseModel, Outcome: agent.OutcomeTimeout, Duration: 120 * time.Second, Model: "nemotron", Attempts: []llm.Attempt{
			{Model: "qwen", Duration: time.Second, Err: errors.New("503")},
			{Model: "nemotron", Duration: 119 * time.Second, Err: context.DeadlineExceeded},
		}},
	}})

	requireLines(t, scrape(t, m),
		`chatops_turn_iterations_count 2`,
		`chatops_turn_iterations_sum 3`,
		`chatops_model_call_duration_seconds_count{model="qwen",outcome="ok",phase="plan"} 1`,
		// Each attempt is observed against its own model and duration.
		`chatops_model_call_duration_seconds_count{model="qwen",outcome="timeout",phase="model"} 1`,
		`chatops_model_call_duration_seconds_sum{model="qwen",outcome="timeout",phase="model"} 30`,
		`chatops_model_call_duration_seconds_count{model="nemotron",outcome="ok",phase="answer"} 1`,
		`chatops_model_call_duration_seconds_sum{model="nemotron",outcome="ok",phase="answer"} 4`,
		`chatops_model_call_duration_seconds_count{model="qwen",outcome="error",phase="model"} 1`,
		`chatops_model_call_duration_seconds_count{model="nemotron",outcome="timeout",phase="model"} 1`,
		`chatops_model_call_duration_seconds_sum{model="nemotron",outcome="timeout",phase="model"} 119`,
		`chatops_model_tokens_total{kind="prompt",model="qwen",phase="plan"} 1200`,
		`chatops_model_tokens_total{kind="completion",model="qwen",phase="plan"} 45`,
		`chatops_model_failovers_total{model="qwen"} 2`,
		`chatops_tool_call_duration_seconds_count{outcome="ok",server="kubernetes",tool="pod_list"} 1`,
		`chatops_tool_call_duration_seconds_count{outcome="error",server="kubernetes",tool="pod_list"} 1`,
		`chatops_tool_call_duration_seconds_count{outcome="error",server="(unknown)",tool="(unknown)"} 1`,
		`chatops_tool_result_bytes_count{server="kubernetes",tool="pod_list"} 1`,
		`chatops_tool_result_bytes_sum{server="kubernetes",tool="pod_list"} 126000`,
		`chatops_tool_result_truncated_total{server="kubernetes",tool="pod_list"} 1`,
	)
}

func Test_Metrics_Finished_no_usage(t *testing.T) {
	m := New("v0")
	m.Finished("slack", agent.Stats{Outcome: agent.OutcomeAnswered, Steps: []agent.Step{{Phase: agent.PhaseAnswer, Outcome: agent.OutcomeOK}}})
	require.NotContains(t, scrape(t, m), "chatops_model_tokens_total{")
}
