package k8s

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

// failList makes the fake dynamic client return err for list calls on resource.
func failList(c *cluster, resource string, err error) {
	fake := c.dynamic.(*dynamicfake.FakeDynamicClient)
	fake.PrependReactor("list", resource, func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, err
	})
}

// testMapper is a RESTMapper covering the built-in types the tests exercise.
func testMapper() meta.RESTMapper {
	m := meta.NewDefaultRESTMapper(nil)
	m.Add(schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, meta.RESTScopeNamespace)
	m.Add(schema.GroupVersionKind{Version: "v1", Kind: "Secret"}, meta.RESTScopeNamespace)
	m.Add(schema.GroupVersionKind{Version: "v1", Kind: "Event"}, meta.RESTScopeNamespace)
	m.Add(schema.GroupVersionKind{Version: "v1", Kind: "Node"}, meta.RESTScopeRoot)
	m.Add(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "StatefulSet"}, meta.RESTScopeNamespace)
	return m
}

func testGVRs() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "pods"}:                        "PodList",
		{Version: "v1", Resource: "secrets"}:                     "SecretList",
		{Version: "v1", Resource: "events"}:                      "EventList",
		{Version: "v1", Resource: "nodes"}:                       "NodeList",
		{Group: "apps", Version: "v1", Resource: "statefulsets"}: "StatefulSetList",
	}
}

// newTestCluster builds a cluster backed by a fake dynamic client seeded with
// objs and the shared test mapper.
func newTestCluster(objs ...runtime.Object) *cluster {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), testGVRs(), objs...)
	return &cluster{dynamic: dyn, mapper: testMapper(), defaultNamespace: "default"}
}

func newPod(namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata":   map[string]any{"name": name, "namespace": namespace, "uid": "uid-" + name},
		"status":     map[string]any{"phase": "Running"},
	}}
}

func newNode(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Node",
		"metadata":   map[string]any{"name": name},
	}}
}

func newEvent(namespace, name, kind, reason, message, last string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion":    "v1",
		"kind":          "Event",
		"metadata":      map[string]any{"name": name, "namespace": namespace},
		"type":          kind,
		"reason":        reason,
		"message":       message,
		"lastTimestamp": last,
	}}
}

func Test_cluster_mappingFor(t *testing.T) {
	c := newTestCluster()
	testCases := map[string]struct {
		input      string
		resource   string
		namespaced bool
		errMsg     string
	}{
		"plural":           {input: "pods", resource: "pods", namespaced: true},
		"singular":         {input: "pod", resource: "pods", namespaced: true},
		"kind":             {input: "Pod", resource: "pods", namespaced: true},
		"grouped-resource": {input: "statefulsets.apps", resource: "statefulsets", namespaced: true},
		"fully-qualified":  {input: "statefulsets.v1.apps", resource: "statefulsets", namespaced: true},
		"cluster-scoped":   {input: "nodes", resource: "nodes", namespaced: false},
		"blank":            {input: "  ", errMsg: "resource kind is required"},
		"unknown":          {input: "widgets", errMsg: `no resource type "widgets"`},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			mapping, err := c.mappingFor(tc.input)
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.resource, mapping.Resource.Resource)
			require.Equal(t, tc.namespaced, mapping.Scope.Name() == meta.RESTScopeNameNamespace)
		})
	}
}

// errMapper is a RESTMapper whose lookups never match by resource and whose
// RESTMapping fails with a plain (non-"no match") error, exercising
// mappingFor's fallthrough error path.
type errMapper struct{}

func (errMapper) KindFor(schema.GroupVersionResource) (schema.GroupVersionKind, error) {
	return schema.GroupVersionKind{}, nil
}

func (errMapper) KindsFor(schema.GroupVersionResource) ([]schema.GroupVersionKind, error) {
	return nil, nil
}

func (errMapper) ResourceFor(schema.GroupVersionResource) (schema.GroupVersionResource, error) {
	return schema.GroupVersionResource{}, nil
}

func (errMapper) ResourcesFor(schema.GroupVersionResource) ([]schema.GroupVersionResource, error) {
	return nil, nil
}

func (errMapper) RESTMapping(schema.GroupKind, ...string) (*meta.RESTMapping, error) {
	return nil, errors.New("discovery failed")
}

func (errMapper) RESTMappings(schema.GroupKind, ...string) ([]*meta.RESTMapping, error) {
	return nil, errors.New("discovery failed")
}

func (errMapper) ResourceSingularizer(resource string) (string, error) { return resource, nil }

func Test_cluster_mappingFor_nonMatchError(t *testing.T) {
	c := &cluster{mapper: errMapper{}}
	_, err := c.mappingFor("gadget")
	require.ErrorContains(t, err, "resolve resource type")
}

// staleMapper reports no match until ready is set, modeling discovery that
// only learns a resource type after its cache is refreshed.
type staleMapper struct {
	meta.RESTMapper
	ready *bool
}

func (m staleMapper) KindFor(r schema.GroupVersionResource) (schema.GroupVersionKind, error) {
	if !*m.ready {
		return schema.GroupVersionKind{}, &meta.NoResourceMatchError{PartialResource: r}
	}
	return m.RESTMapper.KindFor(r)
}

func (m staleMapper) RESTMapping(gk schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	if !*m.ready {
		return nil, &meta.NoKindMatchError{GroupKind: gk}
	}
	return m.RESTMapper.RESTMapping(gk, versions...)
}

func Test_cluster_mappingFor_refreshesOnNoMatch(t *testing.T) {
	ready := false
	resets := 0
	c := &cluster{
		mapper:         staleMapper{RESTMapper: testMapper(), ready: &ready},
		resetDiscovery: func() { resets++; ready = true },
	}

	mapping, err := c.mappingFor("pods")
	require.NoError(t, err)
	require.Equal(t, "pods", mapping.Resource.Resource)
	require.Equal(t, 1, resets)
}

func Test_cluster_mappingFor_noMatchWithoutRefresh(t *testing.T) {
	ready := false
	c := &cluster{mapper: staleMapper{RESTMapper: testMapper(), ready: &ready}}

	_, err := c.mappingFor("pods")
	require.ErrorContains(t, err, `server has no resource type "pods"`)
}

func Test_cluster_namespaceFor(t *testing.T) {
	c := newTestCluster()
	podMapping, err := c.mappingFor("pods")
	require.NoError(t, err)
	nodeMapping, err := c.mappingFor("nodes")
	require.NoError(t, err)

	require.Equal(t, "web", c.namespaceFor(podMapping, "web"))
	require.Equal(t, "default", c.namespaceFor(podMapping, ""))
	require.Equal(t, "", c.namespaceFor(nodeMapping, "web"))

	// With no configured default, a namespaced type falls back to "default".
	empty := &cluster{mapper: c.mapper}
	require.Equal(t, "default", empty.namespaceFor(podMapping, ""))
}

func Test_cluster_list_clusterScoped(t *testing.T) {
	c := newTestCluster(newNode("node-1"), newNode("node-2"))

	list, mapping, err := c.list(context.Background(), "nodes", "", false)
	require.NoError(t, err)
	require.Len(t, list.Items, 2)
	require.Equal(t, meta.RESTScopeNameRoot, mapping.Scope.Name())
}

func Test_cluster_list_unknownKind(t *testing.T) {
	c := newTestCluster()
	_, _, err := c.list(context.Background(), "widgets", "", false)
	require.ErrorContains(t, err, `no resource type "widgets"`)
}

func Test_cluster_list_apiError(t *testing.T) {
	c := newTestCluster()
	failList(c, "pods", errors.New("boom"))
	_, _, err := c.list(context.Background(), "pods", "web", false)
	require.ErrorContains(t, err, "boom")
}

func Test_cluster_events_listError(t *testing.T) {
	pod := newPod("web", "api")
	c := newTestCluster(pod)
	failList(c, "events", errors.New("boom"))
	require.Nil(t, c.events(context.Background(), pod))
}

func Test_cluster_get_unknownKind(t *testing.T) {
	c := newTestCluster()
	_, _, err := c.get(context.Background(), "widgets", "web", "x")
	require.ErrorContains(t, err, `no resource type "widgets"`)
}

func Test_cluster_events_unparsableTimestamp(t *testing.T) {
	pod := newPod("web", "api")
	c := newTestCluster(pod, newEvent("web", "e1", "Normal", "Pulled", "image pulled", "not-a-timestamp"))

	events := c.events(context.Background(), pod)
	require.Len(t, events, 1)
	require.Equal(t, "Pulled", events[0].Reason)
	require.True(t, events[0].Last.IsZero())
}

func Test_cluster_get(t *testing.T) {
	c := newTestCluster(newPod("web", "api"))

	obj, mapping, err := c.get(context.Background(), "pod", "web", "api")
	require.NoError(t, err)
	require.Equal(t, "api", obj.GetName())
	require.Equal(t, "pods", mapping.Resource.Resource)

	_, _, err = c.get(context.Background(), "pod", "web", "missing")
	require.Error(t, err)
}

func Test_cluster_list(t *testing.T) {
	c := newTestCluster(newPod("web", "api"), newPod("web", "worker"), newPod("db", "cache"))

	list, mapping, err := c.list(context.Background(), "pods", "web", false)
	require.NoError(t, err)
	require.Len(t, list.Items, 2)
	require.Equal(t, "pods", mapping.Resource.Resource)

	all, _, err := c.list(context.Background(), "pods", "", true)
	require.NoError(t, err)
	require.Len(t, all.Items, 3)
}

func Test_cluster_events(t *testing.T) {
	pod := newPod("web", "api")
	c := newTestCluster(
		pod,
		newEvent("web", "e1", "Warning", "BackOff", "restarting", "2026-07-23T10:00:00Z"),
		newEvent("web", "e2", "Normal", "Pulled", "image pulled", "2026-07-23T09:00:00Z"),
	)

	events := c.events(context.Background(), pod)
	require.Len(t, events, 2)
	// Sorted oldest first.
	require.Equal(t, "Pulled", events[0].Reason)
	require.Equal(t, "BackOff", events[1].Reason)
	require.Equal(t, time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC), events[0].Last.UTC())

	// A cluster-scoped object has no namespace and yields no events.
	require.Nil(t, c.events(context.Background(), newNode("node-1")))
}
