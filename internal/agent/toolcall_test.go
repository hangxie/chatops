package agent

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/llm"
)

func Test_truncate(t *testing.T) {
	tests := map[string]struct {
		text  string
		limit int
		want  string
	}{
		"under":       {text: "pong", limit: 10, want: "pong"},
		"exact":       {text: "pong", limit: 4, want: "pong"},
		"over":        {text: strings.Repeat("a", 20), limit: 8, want: "aaaaaaaa\n[truncated: showing 8 of 20 bytes]"},
		"rune-safe":   {text: "ab€cd", limit: 3, want: "ab\n[truncated: showing 2 of 7 bytes]"},
		"empty":       {text: "", limit: 1, want: ""},
		"first-rune":  {text: "€", limit: 2, want: "\n[truncated: showing 0 of 3 bytes]"},
		"zero-length": {text: "abc", limit: 0, want: "\n[truncated: showing 0 of 3 bytes]"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, truncate(tc.text, tc.limit))
		})
	}
}

func Test_runTool_logs_arguments_at_debug(t *testing.T) {
	tests := map[string]struct {
		level slog.Level
		call  llm.ToolCall
		want  string
	}{
		"debug":        {level: slog.LevelDebug, call: llm.ToolCall{Name: "builtin__get", Arguments: `{"name":"a"}`}, want: `"msg":"tool call requested","tool":"builtin__get","arguments":"{\"name\":\"a\"}"`},
		"unknown-tool": {level: slog.LevelDebug, call: llm.ToolCall{Name: "builtin__rm", Arguments: `{"path":"/"}`}, want: `"tool":"builtin__rm","arguments":"{\"path\":\"/\"}"`},
		"truncated":    {level: slog.LevelDebug, call: llm.ToolCall{Name: "builtin__get", Arguments: `{"name":"` + strings.Repeat("x", 2000) + `"}`}, want: `[truncated: showing 1024 of 2011 bytes]`},
		"info":         {level: slog.LevelInfo, call: llm.ToolCall{Name: "builtin__get", Arguments: `{"name":"a"}`}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: tc.level}))
			tools := newFakeTools(t, pong)
			New(&fakeModel{}, tools, testLimits(), logger).runTool(context.Background(), tools.Catalog(), tc.call)
			if tc.want == "" {
				require.NotContains(t, logs.String(), "tool call requested")
				return
			}
			require.Contains(t, logs.String(), tc.want)
		})
	}
}
