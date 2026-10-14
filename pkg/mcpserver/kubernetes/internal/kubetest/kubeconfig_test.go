package kubetest

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_WriteKubeconfig(t *testing.T) {
	data, err := os.ReadFile(WriteKubeconfig(t))
	require.NoError(t, err)
	require.Contains(t, string(data), "current-context: ctx1")
}
