package k8s

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

const testKubeconfig = `apiVersion: v1
kind: Config
current-context: alpha
clusters:
- name: c
  cluster:
    server: https://127.0.0.1:6443
contexts:
- name: alpha
  context:
    cluster: c
    user: u
    namespace: team-alpha
- name: beta
  context:
    cluster: c
    user: u
    namespace: team-beta
users:
- name: u
  user:
    token: t
`

func writeKubeconfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	require.NoError(t, os.WriteFile(path, []byte(testKubeconfig), 0o600))
	return path
}

func Test_newCluster(t *testing.T) {
	path := writeKubeconfig(t)

	t.Run("namespace from current context", func(t *testing.T) {
		c, err := newCluster(clusterConfig{kubeconfig: path})
		require.NoError(t, err)
		require.Equal(t, "team-alpha", c.defaultNamespace)
	})

	t.Run("context override selects its namespace", func(t *testing.T) {
		c, err := newCluster(clusterConfig{kubeconfig: path, context: "beta"})
		require.NoError(t, err)
		require.Equal(t, "team-beta", c.defaultNamespace)
	})

	t.Run("missing kubeconfig errors", func(t *testing.T) {
		_, err := newCluster(clusterConfig{kubeconfig: filepath.Join(t.TempDir(), "absent")})
		require.ErrorContains(t, err, "load kubeconfig")
	})
}

func Test_newCluster_clientBuildErrors(t *testing.T) {
	path := writeKubeconfig(t)
	origDynamic, origDiscovery := newDynamicClient, newDiscoveryClient
	defer func() { newDynamicClient, newDiscoveryClient = origDynamic, origDiscovery }()

	t.Run("dynamic client error", func(t *testing.T) {
		newDynamicClient = func(*rest.Config) (*dynamic.DynamicClient, error) { return nil, errors.New("boom") }
		defer func() { newDynamicClient = origDynamic }()
		_, err := newCluster(clusterConfig{kubeconfig: path})
		require.ErrorContains(t, err, "build dynamic client")
	})

	t.Run("discovery client error", func(t *testing.T) {
		newDiscoveryClient = func(*rest.Config) (*discovery.DiscoveryClient, error) { return nil, errors.New("boom") }
		defer func() { newDiscoveryClient = origDiscovery }()
		_, err := newCluster(clusterConfig{kubeconfig: path})
		require.ErrorContains(t, err, "build discovery client")
	})
}
