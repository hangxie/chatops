package kube

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_HumanCPU(t *testing.T) {
	require.Equal(t, "277m", HumanCPU("276202172n")) // MilliValue rounds up from 276.2m
	require.Equal(t, "4000m", HumanCPU("4"))
	require.Equal(t, "", HumanCPU(""))
	require.Equal(t, "bad", HumanCPU("bad"))
}

func Test_Percent(t *testing.T) {
	require.Equal(t, "25%", Percent("4", "16"))
	require.Equal(t, "", Percent("4", "0"))
	require.Equal(t, "", Percent("bad", "16"))
}

func Test_WithPercent(t *testing.T) {
	require.Equal(t, "4 (25%)", WithPercent("4", "25%"))
	require.Equal(t, "4", WithPercent("4", ""))
	require.Equal(t, "", WithPercent("", "25%"))
}
