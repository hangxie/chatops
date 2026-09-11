package k8s

import (
	"context"
	"sync"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// lazyCluster builds the cluster client on first use.
//
// Loading a kubeconfig is I/O and can fail — the host may simply not have one
// — and this group is served by default, so building the client at
// registration would stop a bot that was never meant to talk to Kubernetes
// from starting at all. Deferring it keeps a missing or broken kubeconfig an
// error on the kubernetes tools alone: the requester sees why, and every
// other tool carries on.
//
// Only a successful client is remembered, so a kubeconfig that appears (or is
// fixed) later starts working without restarting the server.
type lazyCluster struct {
	config clusterConfig

	mu     sync.Mutex
	client resourceClient
}

// newLazyCluster returns a resourceClient that resolves cc on first use.
func newLazyCluster(cc clusterConfig) *lazyCluster {
	return &lazyCluster{config: cc}
}

// resolve returns the cluster client, building it if this is the first use.
func (l *lazyCluster) resolve() (resourceClient, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.client != nil {
		return l.client, nil
	}
	client, err := newCluster(l.config)
	if err != nil {
		return nil, err
	}
	l.client = client
	return client, nil
}

func (l *lazyCluster) list(ctx context.Context, kind, namespace string, allNamespaces bool) (*unstructured.UnstructuredList, *meta.RESTMapping, error) {
	client, err := l.resolve()
	if err != nil {
		return nil, nil, err
	}
	return client.list(ctx, kind, namespace, allNamespaces)
}

func (l *lazyCluster) get(ctx context.Context, kind, namespace, name string) (*unstructured.Unstructured, *meta.RESTMapping, error) {
	client, err := l.resolve()
	if err != nil {
		return nil, nil, err
	}
	return client.get(ctx, kind, namespace, name)
}

// events returns nil when the cluster cannot be reached, matching cluster's
// own behaviour: a describe still renders its object without events.
func (l *lazyCluster) events(ctx context.Context, obj *unstructured.Unstructured) []eventInfo {
	client, err := l.resolve()
	if err != nil {
		return nil
	}
	return client.events(ctx, obj)
}
