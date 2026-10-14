package kube

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

func Test_Age(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	kubetest.FreezeNow(t, &Now, base)
	require.Equal(t, "<unknown>", Age(time.Time{}))
	require.Equal(t, "0s", Age(base.Add(time.Hour))) // future clamps to zero
	require.Equal(t, "30s", Age(base.Add(-30*time.Second)))
	require.Equal(t, "5m", Age(base.Add(-5*time.Minute)))
	require.Equal(t, "10h", Age(base.Add(-10*time.Hour)))
	require.Equal(t, "3d", Age(base.Add(-72*time.Hour)))
}

func Test_formatEach(t *testing.T) {
	upper := func(s string) string { return strings.ToUpper(s) }
	require.Equal(t, "A\n\nB", formatEach([]string{"a", "b"}, upper))
	require.Equal(t, "A", formatEach([]string{"a"}, upper))
	require.Empty(t, formatEach(nil, upper))
}

func Test_Table(t *testing.T) {
	require.Equal(t, "A    BB\nccc  d", Table([]string{"A", "BB"}, [][]string{{"ccc", "d"}}))
	require.Equal(t, "A  B", Table([]string{"A", "B"}, nil))
	require.Equal(t, "A    B\nccc", Table([]string{"A", "B"}, [][]string{{"ccc", ""}}))
}
