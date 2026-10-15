package metrics

import (
	"cmp"
	"time"

	"github.com/hangxie/chatops/internal/agent"
)

// unknownLabel stands in for a tool the model named but no server serves; model output never becomes a label.
const unknownLabel = "(unknown)"

func (m *Metrics) Received(backend string) {
	m.received.WithLabelValues(backend).Inc()
}

func (m *Metrics) Rejected(backend, reason string) {
	m.rejected.WithLabelValues(backend, reason).Inc()
}

func (m *Metrics) Started(backend string, queued time.Duration) {
	m.queueWait.WithLabelValues(backend).Observe(queued.Seconds())
}

func (m *Metrics) Delivered(backend string, outcome agent.Outcome, elapsed time.Duration) {
	m.response.WithLabelValues(backend, string(outcome)).Observe(elapsed.Seconds())
}

func (m *Metrics) ReplyFailed(backend string) {
	m.replyFailures.WithLabelValues(backend).Inc()
}

// Finished records the turn's steps; backend is unused because step metrics are backend-agnostic.
func (m *Metrics) Finished(_ string, stats agent.Stats) {
	models := 0
	for _, step := range stats.Steps {
		if step.Phase == agent.PhaseTool {
			m.tool(step)
			continue
		}
		models++
		phase := string(step.Phase)
		m.modelCall.WithLabelValues(phase, string(step.Outcome)).Observe(step.Duration.Seconds())
		if step.Usage != nil {
			m.tokens.WithLabelValues(phase, "prompt").Add(float64(step.Usage.PromptTokens))
			m.tokens.WithLabelValues(phase, "completion").Add(float64(step.Usage.CompletionTokens))
		}
	}
	m.iterations.Observe(float64(models))
}

func (m *Metrics) tool(step agent.Step) {
	server, tool := cmp.Or(step.Server, unknownLabel), unknownLabel
	if step.Server != "" {
		tool = step.Tool
	}
	m.toolCall.WithLabelValues(server, tool, string(step.Outcome)).Observe(step.Duration.Seconds())
	if step.ResultBytes > 0 || step.Outcome == agent.OutcomeOK {
		m.toolBytes.WithLabelValues(server, tool).Observe(float64(step.ResultBytes))
	}
	if step.Truncated {
		m.truncated.WithLabelValues(server, tool).Inc()
	}
}
