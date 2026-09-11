package k8s

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func Test_lazyCluster_resolves_once(t *testing.T) {
	path := writeKubeconfig(t)
	lazy := newLazyCluster(clusterConfig{kubeconfig: path, context: "alpha"})

	first, err := lazy.resolve()
	require.NoError(t, err)
	require.NotNil(t, first)

	// A resolved client is remembered, so every later call reuses it.
	second, err := lazy.resolve()
	require.NoError(t, err)
	require.Same(t, first, second)
}

func Test_lazyCluster_retries_after_failure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	lazy := newLazyCluster(clusterConfig{kubeconfig: missing, context: "nope"})

	_, err := lazy.resolve()
	require.ErrorContains(t, err, "load kubeconfig")

	// A failure is not remembered: pointing the config at a usable file makes
	// the tools work without restarting the server.
	lazy.config = clusterConfig{kubeconfig: writeKubeconfig(t), context: "alpha"}
	client, err := lazy.resolve()
	require.NoError(t, err)
	require.NotNil(t, client)
}

func Test_lazyCluster_reports_failure_per_call(t *testing.T) {
	lazy := newLazyCluster(clusterConfig{kubeconfig: filepath.Join(t.TempDir(), "missing.yaml"), context: "nope"})
	ctx := context.Background()

	_, _, err := lazy.list(ctx, "pods", "", false)
	require.ErrorContains(t, err, "load kubeconfig")

	_, _, err = lazy.get(ctx, "pod", "", "api")
	require.ErrorContains(t, err, "load kubeconfig")

	// Events are best-effort, so an unreachable cluster yields none rather
	// than an error, matching cluster's own behaviour.
	require.Nil(t, lazy.events(ctx, newPod("web", "api")))
}

func Test_lazyCluster_delegates_to_the_resolved_client(t *testing.T) {
	stub := &fakeClient{
		listFn: func(context.Context, string, string, bool) (*unstructured.UnstructuredList, *meta.RESTMapping, error) {
			return &unstructured.UnstructuredList{}, podMapping(t), nil
		},
		getFn: func(_ context.Context, _, _, name string) (*unstructured.Unstructured, *meta.RESTMapping, error) {
			return newPod("web", name), nil, nil
		},
		eventsFn: func(context.Context, *unstructured.Unstructured) []eventInfo {
			return []eventInfo{{Reason: "Started"}}
		},
	}
	lazy := &lazyCluster{client: stub}
	ctx := context.Background()

	_, _, err := lazy.list(ctx, "pods", "", false)
	require.NoError(t, err)

	obj, _, err := lazy.get(ctx, "pod", "", "api")
	require.NoError(t, err)
	require.Equal(t, "api", obj.GetName())

	require.Len(t, lazy.events(ctx, obj), 1)
}

func Test_lazyCluster_propagates_client_errors(t *testing.T) {
	boom := errors.New("boom")
	lazy := &lazyCluster{client: &fakeClient{
		listFn: func(context.Context, string, string, bool) (*unstructured.UnstructuredList, *meta.RESTMapping, error) {
			return nil, nil, boom
		},
		getFn: func(context.Context, string, string, string) (*unstructured.Unstructured, *meta.RESTMapping, error) {
			return nil, nil, boom
		},
	}}
	ctx := context.Background()

	_, _, err := lazy.list(ctx, "pods", "", false)
	require.ErrorIs(t, err, boom)

	_, _, err = lazy.get(ctx, "pod", "", "api")
	require.ErrorIs(t, err, boom)
}
