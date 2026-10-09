package mcp

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_modelName(t *testing.T) {
	long := strings.Repeat("t", 70)
	tests := map[string]struct {
		server string
		tool   string
		want   string
	}{
		"plain":        {server: "builtin", tool: "ping", want: "builtin__ping"},
		"dash-kept":    {server: "k8s-lab", tool: "list-pods", want: "k8s-lab__list-pods"},
		"dots-hashed":  {server: "builtin", tool: "pods.list", want: "builtin__pods_list_" + shortHash("builtin", "pods.list")},
		"slash-hashed": {server: "builtin", tool: "a/b", want: "builtin__a_b_" + shortHash("builtin", "a/b")},
		"too-long":     {server: "builtin", tool: long, want: ("builtin__" + long)[:55] + "_" + shortHash("builtin", long)},
		"exactly-64":   {server: "s", tool: strings.Repeat("x", 61), want: "s__" + strings.Repeat("x", 61)},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := modelName(tc.server, tc.tool)
			require.Equal(t, tc.want, got)
			require.LessOrEqual(t, len(got), 64)
			require.Regexp(t, `^[a-zA-Z0-9_-]{1,64}$`, got)
		})
	}
}

func Test_shortHash_distinguishes_identity(t *testing.T) {
	require.Len(t, shortHash("a", "b"), 8)
	require.NotEqual(t, shortHash("a", "b.c"), shortHash("a.b", "c"))
	require.NotEqual(t, modelName("s", "a.b"), modelName("s", "a/b"))
}
