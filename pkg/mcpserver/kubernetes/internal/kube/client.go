// Package kube holds what every kubernetes resource package shares: clients, targets, the per-target
// handler, and the pod, describe, and table helpers.
package kube

import (
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

// Config selects how the Kubernetes client connects; an empty Config uses in-cluster then the default kubeconfig.
type Config struct {
	Kubeconfig string
	Context    string
	// MaxResults caps the rows of every list and pods tool; zero means DefaultMaxResults.
	MaxResults int
}

// Clients holds the API clients every resource shares, built once from one rest.Config, and the row cap.
type Clients struct {
	Core       k8s.Interface
	Metrics    metricsclient.Interface
	MaxResults int
}

// NewClients resolves the connection, applies the request-timeout backstop and user agent, and builds the clients.
func NewClients(cfg Config, timeout time.Duration, userAgent string) (Clients, error) {
	if cfg.MaxResults < 0 {
		return Clients{}, fmt.Errorf("max results must not be negative, got %d", cfg.MaxResults)
	}
	restCfg, err := restConfig(cfg)
	if err != nil {
		return Clients{}, err
	}
	restCfg.Timeout = timeout
	restCfg.UserAgent = userAgent
	core, err := k8s.NewForConfig(restCfg)
	if err != nil {
		return Clients{}, fmt.Errorf("kubernetes client: %w", err)
	}
	metrics, err := metricsclient.NewForConfig(restCfg)
	if err != nil {
		return Clients{}, fmt.Errorf("metrics client: %w", err)
	}
	return Clients{Core: core, Metrics: metrics, MaxResults: cfg.MaxResults}, nil
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
