package engine

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/tool"
)

func Test_canonicalContent(t *testing.T) {
	testCases := map[string]struct {
		result tool.Result
		want   string
	}{
		"text only":      {result: tool.Result{Text: "hello"}, want: "hello"},
		"empty":          {result: tool.Result{}, want: ""},
		"details sorted": {result: tool.Result{Details: map[string]string{"b": "2", "a": "1"}}, want: "a=1\nb=2"},
		"text and details": {
			result: tool.Result{Text: "line", Details: map[string]string{"z": "9", "a": "1"}},
			want:   "line\na=1\nz=9",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, canonicalContent(tc.result))
		})
	}
}

func Test_boundContent(t *testing.T) {
	t.Run("under limit is unchanged", func(t *testing.T) {
		require.Equal(t, "short", boundContent("short", 100))
	})

	t.Run("zero limit is empty", func(t *testing.T) {
		require.Equal(t, "", boundContent("anything", 0))
	})

	t.Run("over limit stays within the limit and marks truncation", func(t *testing.T) {
		s := strings.Repeat("a", 500)
		got := boundContent(s, 100)
		require.LessOrEqual(t, len(got), 100)
		require.Contains(t, got, "truncated")
		require.Contains(t, got, "500 bytes total")
	})

	t.Run("never splits a multibyte rune", func(t *testing.T) {
		// The marker itself is valid UTF-8, so a mid-rune cut would only come
		// from the kept prefix; a marker-bearing result is still fully valid.
		s := strings.Repeat("€", 200) // 600 bytes
		got := boundContent(s, 137)
		require.LessOrEqual(t, len(got), 137)
		require.True(t, utf8.ValidString(got), "result must be valid UTF-8")
	})

	t.Run("boundary at or past the end returns the whole string", func(t *testing.T) {
		require.Equal(t, 3, runeBoundary("abc", 5))
	})

	t.Run("limit too small for a marker hard-cuts on a rune boundary", func(t *testing.T) {
		s := strings.Repeat("€", 50) // 150 bytes
		got := boundContent(s, 10)
		require.LessOrEqual(t, len(got), 10)
		require.True(t, utf8.ValidString(got))
		require.NotContains(t, got, "truncated") // no room for a marker
	})
}
