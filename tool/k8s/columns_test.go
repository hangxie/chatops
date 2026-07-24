package k8s

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// podWith builds a pod carrying container statuses so the READY and RESTARTS
// columns have data to extract.
func podWith(statuses ...map[string]any) *unstructured.Unstructured {
	items := make([]any, len(statuses))
	for i, s := range statuses {
		items[i] = s
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata":   map[string]any{"name": "web", "namespace": "app"},
		"status": map[string]any{
			"phase":             "Running",
			"containerStatuses": items,
		},
	}}
}

func deploymentMapping() *meta.RESTMapping {
	return &meta.RESTMapping{
		Resource:         schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
		GroupVersionKind: schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
		Scope:            meta.RESTScopeNamespace,
	}
}

func Test_podReady(t *testing.T) {
	testCases := map[string]struct {
		obj  *unstructured.Unstructured
		want string
	}{
		"all ready":   {obj: podWith(map[string]any{"ready": true}, map[string]any{"ready": true}), want: "2/2"},
		"partial":     {obj: podWith(map[string]any{"ready": true}, map[string]any{"ready": false}), want: "1/2"},
		"no statuses": {obj: newPod("app", "web"), want: ""},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, podReady(tc.obj))
		})
	}
}

func Test_podRestarts(t *testing.T) {
	testCases := map[string]struct {
		obj  *unstructured.Unstructured
		want string
	}{
		"summed":      {obj: podWith(map[string]any{"restartCount": int64(2)}, map[string]any{"restartCount": int64(3)}), want: "5"},
		"float":       {obj: podWith(map[string]any{"restartCount": float64(4)}), want: "4"},
		"none":        {obj: podWith(map[string]any{"ready": true}), want: "0"},
		"no statuses": {obj: newPod("app", "web"), want: ""},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, podRestarts(tc.obj))
		})
	}
}

func Test_statusValue(t *testing.T) {
	phase := newPod("app", "web")
	health := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{"health": map[string]any{"status": "Healthy"}},
	}}
	none := &unstructured.Unstructured{Object: map[string]any{}}

	require.Equal(t, "Running", statusValue(phase))
	require.Equal(t, "Healthy", statusValue(health))
	require.Equal(t, "", statusValue(none))
}

func Test_deploymentReady(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"spec":   map[string]any{"replicas": int64(3)},
		"status": map[string]any{"readyReplicas": int64(1)},
	}}
	require.Equal(t, "1/3", deploymentReady(obj))

	empty := &unstructured.Unstructured{Object: map[string]any{}}
	require.Equal(t, "0/0", deploymentReady(empty))
}

func Test_valueColumns(t *testing.T) {
	c := newTestCluster()
	podMap, err := c.mappingFor("pods")
	require.NoError(t, err)
	names := func(cols []column) []string {
		out := make([]string, len(cols))
		for i, col := range cols {
			out[i] = col.name
		}
		return out
	}

	require.Equal(t, []string{"READY", "STATUS", "RESTARTS"}, names(valueColumns(podMap)))
	require.Equal(t, []string{"READY", "UP-TO-DATE", "AVAILABLE"}, names(valueColumns(deploymentMapping())))

	nodeMap, err := c.mappingFor("nodes")
	require.NoError(t, err)
	require.Equal(t, []string{"STATUS"}, names(valueColumns(nodeMap)))
}
