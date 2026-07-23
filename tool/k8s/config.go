package k8s

import (
	"fmt"

	"k8s.io/client-go/discovery"
	memory "k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
)

// clusterConfig captures how to reach a cluster, parsed from the tool URL.
// It carries no credentials: the kubeconfig (or in-cluster service account)
// referenced through the standard loading rules supplies the API server URL,
// CA bundle, and client certificate or token.
type clusterConfig struct {
	// kubeconfig optionally overrides the kubeconfig path. When empty the
	// standard loading rules apply (the KUBECONFIG environment variable,
	// then ~/.kube/config), falling back to in-cluster config.
	kubeconfig string

	// context selects a kubeconfig context; empty uses the current context
	// (or in-cluster config when running inside a pod).
	context string
}

// Client constructors, as package variables so tests can exercise their
// failure paths; production always uses the client-go implementations.
var (
	newDynamicClient   = dynamic.NewForConfig
	newDiscoveryClient = discovery.NewDiscoveryClientForConfig
)

// newCluster builds a cluster client from cc. Client construction performs no
// network I/O: discovery is deferred until the first call that needs to map a
// resource type, so an unreachable API server surfaces at Invoke, not Open.
func newCluster(cc clusterConfig) (*cluster, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if cc.kubeconfig != "" {
		rules.ExplicitPath = cc.kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{}
	if cc.context != "" {
		overrides.CurrentContext = cc.context
	}
	loader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)

	config, err := loader.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("k8s: load kubeconfig: %w", err)
	}

	dyn, err := newDynamicClient(config)
	if err != nil {
		return nil, fmt.Errorf("k8s: build dynamic client: %w", err)
	}
	discoveryClient, err := newDiscoveryClient(config)
	if err != nil {
		return nil, fmt.Errorf("k8s: build discovery client: %w", err)
	}
	deferred := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(discoveryClient))
	mapper := restmapper.NewShortcutExpander(deferred, discoveryClient, func(string) {})

	// The default namespace for calls that omit one comes from the selected
	// kubeconfig context; namespaceFor falls back to "default" when unset.
	var namespace string
	if ns, _, nsErr := loader.Namespace(); nsErr == nil {
		namespace = ns
	}

	return &cluster{
		dynamic:          dyn,
		mapper:           mapper,
		defaultNamespace: namespace,
		resetDiscovery:   deferred.Reset,
	}, nil
}
