package conversation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/agent"
	"github.com/hangxie/chatops/internal/llm"
)

func Test_footer(t *testing.T) {
	plan := agent.Step{Phase: agent.PhasePlan, Outcome: agent.OutcomeOK, Duration: 2300 * time.Millisecond, Usage: &llm.Usage{PromptTokens: 1200, CompletionTokens: 45}}
	tool := agent.Step{Phase: agent.PhaseTool, Outcome: agent.OutcomeOK, Duration: 5700 * time.Millisecond, Name: "builtin__pod_list", Server: "builtin", Tool: "pod_list", ResultBytes: 123 * 1024, SentBytes: 16 * 1024}
	answer := agent.Step{Phase: agent.PhaseAnswer, Outcome: agent.OutcomeOK, Duration: 6300 * time.Millisecond, Usage: &llm.Usage{PromptTokens: 8100, CompletionTokens: 310}}
	tests := map[string]struct {
		stats      agent.Stats
		replyBytes int
		want       string
	}{
		"answered": {
			stats:      agent.Stats{Outcome: agent.OutcomeAnswered, Duration: 14300 * time.Millisecond, Steps: []agent.Step{plan, tool, answer}},
			replyBytes: 2150,
			want:       "plan 2.3s (1.2k→45 tok) → builtin.pod_list 5.7s (123KB→16KB) → answer 6.3s (8.1k→310 tok)\ntotal 14.3s · 1 tool · 9.3k→355 tok · reply 2.1KB",
		},
		"no-tools-no-usage": {
			stats:      agent.Stats{Outcome: agent.OutcomeAnswered, Duration: 350 * time.Millisecond, Steps: []agent.Step{{Phase: agent.PhaseAnswer, Outcome: agent.OutcomeOK, Duration: 350 * time.Millisecond}}},
			replyBytes: 12,
			want:       "answer 350ms\ntotal 350ms · 0 tools · reply 12B",
		},
		"untruncated-tools": {
			stats: agent.Stats{Outcome: agent.OutcomeAnswered, Duration: 3 * time.Second, Steps: []agent.Step{
				{Phase: agent.PhaseTool, Outcome: agent.OutcomeOK, Duration: time.Second, Name: "builtin__ping", Server: "builtin", Tool: "ping", ResultBytes: 4, SentBytes: 4},
				{Phase: agent.PhaseTool, Outcome: agent.OutcomeError, Duration: time.Second, Name: "builtin__ping", Server: "builtin", Tool: "ping", ResultBytes: 12, SentBytes: 19},
			}},
			replyBytes: 3 << 20,
			want:       "builtin.ping 1.0s (4B) → builtin.ping 1.0s (12B, error)\ntotal 3.0s · 2 tools · reply 3.0MB",
		},
		"rejected-tool": {
			stats: agent.Stats{Outcome: agent.OutcomeAnswered, Duration: time.Second, Steps: []agent.Step{
				{Phase: agent.PhaseTool, Outcome: agent.OutcomeError, Duration: time.Millisecond, Name: "builtin__rm", SentBytes: 33},
			}},
			replyBytes: 10,
			want:       "builtin__rm 1ms (error)\ntotal 1.0s · 1 tool · reply 10B",
		},
		"timeout": {
			stats: agent.Stats{Outcome: agent.OutcomeTimeout, Duration: 32300 * time.Millisecond, Steps: []agent.Step{
				plan,
				{Phase: agent.PhaseTool, Outcome: agent.OutcomeTimeout, Duration: 30 * time.Second, Name: "builtin__pod_list", Server: "builtin", Tool: "pod_list", SentBytes: 37},
			}},
			replyBytes: 66,
			want:       "plan 2.3s (1.2k→45 tok) → builtin.pod_list 30.0s (timeout)\ntotal 32.3s · 1 tool · 1.2k→45 tok · reply 66B · timed out",
		},
		"model-error": {
			stats:      agent.Stats{Outcome: agent.OutcomeError, Duration: 20 * time.Millisecond, Steps: []agent.Step{{Phase: agent.PhaseModel, Outcome: agent.OutcomeError, Duration: 20 * time.Millisecond}}},
			replyBytes: 68,
			want:       "model 20ms (error)\ntotal 20ms · 0 tools · reply 68B · failed",
		},
		"stopped": {
			stats:      agent.Stats{Outcome: agent.OutcomeStopped, Duration: 1500 * time.Millisecond, Steps: []agent.Step{{Phase: agent.PhasePlan, Outcome: agent.OutcomeStopped, Duration: 1500 * time.Millisecond}}},
			replyBytes: 80,
			want:       "plan 1.5s (stopped)\ntotal 1.5s · 0 tools · reply 80B · stopped",
		},
		"no-steps": {
			stats:      agent.Stats{Outcome: agent.OutcomeError},
			replyBytes: 68,
			want:       "total 0ms · 0 tools · reply 68B · failed",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, footer(tc.stats, tc.replyBytes))
		})
	}
}

func Test_footer_units(t *testing.T) {
	tests := map[string]struct {
		got  string
		want string
	}{
		"zero":       {got: formatDuration(0), want: "0ms"},
		"sub-ms":     {got: formatDuration(400 * time.Microsecond), want: "<1ms"},
		"ms":         {got: formatDuration(999 * time.Millisecond), want: "999ms"},
		"seconds":    {got: formatDuration(1049 * time.Millisecond), want: "1.0s"},
		"bytes":      {got: formatBytes(1023), want: "1023B"},
		"small-kb":   {got: formatBytes(1024), want: "1.0KB"},
		"large-kb":   {got: formatBytes(10 * 1024), want: "10KB"},
		"mb":         {got: formatBytes(1536 * 1024), want: "1.5MB"},
		"large-mb":   {got: formatBytes(12 << 20), want: "12MB"},
		"tokens":     {got: formatCount(999), want: "999"},
		"kilotokens": {got: formatCount(1000), want: "1.0k"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.got)
		})
	}
}
