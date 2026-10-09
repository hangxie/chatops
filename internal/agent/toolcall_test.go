package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
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
