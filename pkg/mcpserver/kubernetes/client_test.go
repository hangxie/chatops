package kubernetes

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
)

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

func writeKubeconfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(path, []byte(twoContextKubeconfig), 0o600))
	return path
}

func stubInCluster(t *testing.T, cfg *rest.Config, err error) {
	t.Helper()
	inClusterConfig = func() (*rest.Config, error) { return cfg, err }
	t.Cleanup(func() { inClusterConfig = rest.InClusterConfig })
}

func Test_restConfig_explicit_kubeconfig(t *testing.T) {
	path := writeKubeconfig(t)
	// An explicit kubeconfig must be honored even when in-cluster would otherwise win.
	stubInCluster(t, &rest.Config{Host: "https://in-cluster"}, nil)

	cfg, err := restConfig(Config{Kubeconfig: path})
	require.NoError(t, err)
	require.Equal(t, "https://ctx1.example:6443", cfg.Host)
}

func Test_restConfig_explicit_kubeconfig_missing(t *testing.T) {
	_, err := restConfig(Config{Kubeconfig: "/no/such/kubeconfig"})
	require.Error(t, err)
}

func Test_restConfig_context_forces_kubeconfig(t *testing.T) {
	t.Setenv("KUBECONFIG", writeKubeconfig(t))
	// A context takes precedence over in-cluster detection.
	stubInCluster(t, &rest.Config{Host: "https://in-cluster"}, nil)

	cfg, err := restConfig(Config{Context: "ctx2"})
	require.NoError(t, err)
	require.Equal(t, "https://ctx2.example:6443", cfg.Host)
}

func Test_restConfig_in_cluster(t *testing.T) {
	stubInCluster(t, &rest.Config{Host: "https://in-cluster:6443"}, nil)

	cfg, err := restConfig(Config{})
	require.NoError(t, err)
	require.Equal(t, "https://in-cluster:6443", cfg.Host)
}

func Test_restConfig_in_cluster_error(t *testing.T) {
	stubInCluster(t, nil, errors.New("boom"))

	_, err := restConfig(Config{})
	require.ErrorContains(t, err, "in-cluster config")
}

func Test_restConfig_dev_fallback(t *testing.T) {
	t.Setenv("KUBECONFIG", writeKubeconfig(t))
	stubInCluster(t, nil, rest.ErrNotInCluster)

	cfg, err := restConfig(Config{})
	require.NoError(t, err)
	require.Equal(t, "https://ctx1.example:6443", cfg.Host)
}

func Test_newClients(t *testing.T) {
	path := writeKubeconfig(t)

	c, err := newClients(Config{Kubeconfig: path}, 5*time.Second, "chatops-mcp/test")
	require.NoError(t, err)
	require.NotNil(t, c.core)
	require.NotNil(t, c.metrics)
	require.NotNil(t, c.diskUsage) // always attempted; RBAC decides
}

func Test_newClients_bad_kubeconfig(t *testing.T) {
	_, err := newClients(Config{Kubeconfig: "/no/such/kubeconfig"}, 5*time.Second, "chatops-mcp/test")
	require.Error(t, err)
}
