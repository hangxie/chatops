package kube

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

func stubInCluster(t *testing.T, cfg *rest.Config, err error) {
	t.Helper()
	inClusterConfig = func() (*rest.Config, error) { return cfg, err }
	t.Cleanup(func() { inClusterConfig = rest.InClusterConfig })
}

func Test_restConfig_explicit_kubeconfig(t *testing.T) {
	path := kubetest.WriteKubeconfig(t)
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
	t.Setenv("KUBECONFIG", kubetest.WriteKubeconfig(t))
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
	t.Setenv("KUBECONFIG", kubetest.WriteKubeconfig(t))
	stubInCluster(t, nil, rest.ErrNotInCluster)

	cfg, err := restConfig(Config{})
	require.NoError(t, err)
	require.Equal(t, "https://ctx1.example:6443", cfg.Host)
}

func Test_NewClients(t *testing.T) {
	path := kubetest.WriteKubeconfig(t)

	c, err := NewClients(Config{Kubeconfig: path}, 5*time.Second, "chatops-mcp/test")
	require.NoError(t, err)
	require.NotNil(t, c.Core)
	require.NotNil(t, c.Metrics)
}

func Test_NewClients_bad_kubeconfig(t *testing.T) {
	_, err := NewClients(Config{Kubeconfig: "/no/such/kubeconfig"}, 5*time.Second, "chatops-mcp/test")
	require.Error(t, err)
}
