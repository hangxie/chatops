package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/llm"
)

func Test_canonicalJSON(t *testing.T) {
	tests := map[string]struct {
		raw  string
		want string
	}{
		"key order":     {`{"b":1, "a":[2, 3]}`, `{"a":[2,3],"b":1}`},
		"large integer": {`{"n":12345678901234567890}`, `{"n":12345678901234567890}`},
		"empty object":  {`{}`, `{}`},
		"invalid":       {`{"a":`, `{"a":`},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, canonicalJSON(tc.raw))
		})
	}
}

func Test_failureLine(t *testing.T) {
	tests := map[string]struct {
		step    Step
		failure string
		want    string
	}{
		"server error":     {Step{Server: "kubernetes", Tool: "node_list"}, "Unauthorized", "`kubernetes.node_list`: Unauthorized"},
		"first line only":  {Step{Server: "s", Tool: "t"}, "boom\nstack trace", "`s.t`: boom"},
		"trimmed":          {Step{Server: "s", Tool: "t"}, "  boom  ", "`s.t`: boom"},
		"long":             {Step{Server: "s", Tool: "t"}, strings.Repeat("é", maxFailureLineRunes+5), "`s.t`: " + strings.Repeat("é", maxFailureLineRunes-1) + "…"},
		"not reportable":   {Step{Server: "s", Tool: "t"}, "", ""},
		"unknown tool":     {Step{Name: "x__y"}, "", ""},
		"exactly at limit": {Step{Server: "s", Tool: "t"}, strings.Repeat("a", maxFailureLineRunes), "`s.t`: " + strings.Repeat("a", maxFailureLineRunes)},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, failureLine(tc.step, tc.failure))
		})
	}
}

func Test_failureLog(t *testing.T) {
	prod := llm.ToolCall{ID: "1", Name: "k__pod_list", Arguments: `{"namespace":"prod"}`}
	dev := llm.ToolCall{ID: "2", Name: "k__pod_list", Arguments: `{"namespace":"dev"}`}
	prodAgain := llm.ToolCall{ID: "3", Name: "k__pod_list", Arguments: `{ "namespace": "prod" }`}
	invalid := llm.ToolCall{ID: "4", Name: "k__pod_list", Arguments: `{"namespace":1}`}

	type event struct {
		call llm.ToolCall
		line string // empty with ok false is an unreportable failure
		ok   bool
	}
	tests := map[string]struct {
		events     []event
		unresolved []string
		repeats    []llm.ToolCall
	}{
		"success elsewhere keeps the failure": {
			events:     []event{{call: prod, line: "403"}, {call: dev, ok: true}},
			unresolved: []string{"403"},
		},
		"same call succeeding resolves it": {
			events: []event{{call: prod, line: "403"}, {call: prodAgain, ok: true}},
		},
		"latest error wins": {
			events:     []event{{call: prod, line: "timeout"}, {call: prodAgain, line: "403"}},
			unresolved: []string{"403"},
			repeats:    []llm.ToolCall{prod},
		},
		"order of first failure": {
			events:     []event{{call: dev, line: "a"}, {call: prod, line: "b"}, {call: dev, line: "c"}},
			unresolved: []string{"c", "b"},
			repeats:    []llm.ToolCall{dev},
		},
		"failing again after resolving": {
			events:     []event{{call: prod, line: "a"}, {call: prod, ok: true}, {call: dev, line: "b"}, {call: prod, line: "c"}},
			unresolved: []string{"b", "c"},
		},
		"invalid arguments count but are not listed": {
			events:  []event{{call: invalid}, {call: invalid}},
			repeats: []llm.ToolCall{invalid},
		},
		"one failure does not repeat": {
			events:     []event{{call: prod, line: "a"}},
			unresolved: []string{"a"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			log := newFailureLog()
			for _, e := range tc.events {
				if e.ok {
					log.succeed(e.call)
				} else {
					log.fail(e.call, e.line)
				}
			}
			require.Equal(t, tc.unresolved, log.unresolved())
			for _, call := range []llm.ToolCall{prod, dev, invalid} {
				want := false
				for _, r := range tc.repeats {
					want = want || keyOf(r) == keyOf(call)
				}
				require.Equal(t, want, log.repeats([]llm.ToolCall{call}), call.Arguments)
			}
		})
	}
}

func Test_withFailures(t *testing.T) {
	tests := map[string]struct {
		reply string
		lines []string
		want  string
	}{
		"no failures":  {"done", nil, "done"},
		"one failure":  {"done", []string{"`s.t`: boom"}, "done\n\nTool errors:\n- `s.t`: boom"},
		"two failures": {"done", []string{"a", "b"}, "done\n\nTool errors:\n- a\n- b"},
		"empty reply":  {"", []string{"a"}, "Tool errors:\n- a"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, withFailures(tc.reply, tc.lines))
		})
	}
}
