package kubetest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// twoContextKubeconfig has contexts ctx1 (current) and ctx2 pointing at unreachable hosts.
const twoContextKubeconfig = `apiVersion: v1
kind: Config
clusters:
- cluster: {server: https://ctx1.example:6443}
  name: c1
- cluster: {server: https://ctx2.example:6443}
  name: c2
contexts:
- context: {cluster: c1, user: u1}
  name: ctx1
- context: {cluster: c2, user: u2}
  name: ctx2
current-context: ctx1
users:
- name: u1
  user: {token: t1}
- name: u2
  user: {token: t2}
`

// WriteKubeconfig writes twoContextKubeconfig to a temporary file and returns its path.
func WriteKubeconfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(path, []byte(twoContextKubeconfig), 0o600))
	return path
}
