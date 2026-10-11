package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"time"

	k8s "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
)

// inClusterConfig is a seam so tests can exercise the in-cluster branch without running in a pod.
var inClusterConfig = rest.InClusterConfig

// clients holds the API clients the node tools share; they are built once from one rest.Config.
// diskUsage is nil unless the kubelet Summary API capability is enabled, so node_resources can degrade cleanly.
type clients struct {
	core      k8s.Interface
	metrics   metricsclient.Interface
	diskUsage func(ctx context.Context, node string) (usedBytes, totalBytes int64, ok bool, err error)
}

// newClients resolves the connection, applies the request-timeout backstop and user agent, and builds the clients.
func newClients(cfg Config, timeout time.Duration, userAgent string) (clients, error) {
	restCfg, err := restConfig(cfg)
	if err != nil {
		return clients{}, err
	}
	restCfg.Timeout = timeout
	restCfg.UserAgent = userAgent
	core, err := k8s.NewForConfig(restCfg)
	if err != nil {
		return clients{}, fmt.Errorf("kubernetes client: %w", err)
	}
	metrics, err := metricsclient.NewForConfig(restCfg)
	if err != nil {
		return clients{}, fmt.Errorf("metrics client: %w", err)
	}
	c := clients{core: core, metrics: metrics}
	if cfg.NodeDiskUsage {
		c.diskUsage = summaryDiskUsage(core.CoreV1().RESTClient())
	}
	return c, nil
}

// restConfig resolves the connection: explicit kubeconfig (no fallback), else an explicit context forces
// kubeconfig, else in-cluster when running as a pod, else the default local kubeconfig for development.
func restConfig(cfg Config) (*rest.Config, error) {
	if cfg.Kubeconfig != "" {
		return kubeconfigRESTConfig(cfg.Kubeconfig, cfg.Context)
	}
	if cfg.Context != "" {
		return kubeconfigRESTConfig("", cfg.Context)
	}
	inCluster, err := inClusterConfig()
	if err == nil {
		return inCluster, nil
	}
	if !errors.Is(err, rest.ErrNotInCluster) {
		return nil, fmt.Errorf("in-cluster config: %w", err)
	}
	return kubeconfigRESTConfig("", "")
}

func kubeconfigRESTConfig(path, context string) (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if path != "" {
		rules = &clientcmd.ClientConfigLoadingRules{ExplicitPath: path}
	}
	overrides := &clientcmd.ConfigOverrides{}
	if context != "" {
		overrides.CurrentContext = context
	}
	restCfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	return restCfg, nil
}
