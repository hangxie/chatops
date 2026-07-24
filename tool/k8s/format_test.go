package k8s

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/hangxie/chatops/tool"
)

func Test_validateOutput_unknownIsUserFacing(t *testing.T) {
	require.NoError(t, validateOutput("json"))
	err := validateOutput("xml")
	_, ok := tool.UserMessage(err)
	require.True(t, ok)
}

func Test_formatObjects_marshalError(t *testing.T) {
	// A non-finite float cannot be encoded as JSON, and yaml goes through JSON.
	objs := []*unstructured.Unstructured{{Object: map[string]any{"x": math.NaN()}}}
	events := [][]eventInfo{nil}
	_, err := formatObjects(objs, events, "json")
	require.Error(t, err)
	_, err = formatObjects(objs, events, "yaml")
	require.Error(t, err)
}

func Test_formatObjects(t *testing.T) {
	pod := newPod("web", "api")
	objs := []*unstructured.Unstructured{pod}
	events := [][]eventInfo{{{Type: "Warning", Reason: "BackOff", Message: "restarting"}}}

	testCases := map[string]struct {
		output   string
		contains []string
		errMsg   string
	}{
		"brief":   {output: "brief", contains: []string{"Pod: api", "Namespace: web", "Status: Running", "Warning BackOff: restarting"}},
		"default": {output: "", contains: []string{"Pod: api"}},
		"json":    {output: "json", contains: []string{`"kind": "Pod"`, `"name": "api"`}},
		"yaml":    {output: "yaml", contains: []string{"kind: Pod", "name: api"}},
		"unknown": {output: "xml", errMsg: "unknown output"},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			text, err := formatObjects(objs, events, tc.output)
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			for _, want := range tc.contains {
				require.Contains(t, text, want)
			}
		})
	}
}

func Test_formatObjects_multiple(t *testing.T) {
	objs := []*unstructured.Unstructured{newPod("web", "a"), newPod("web", "b")}
	events := [][]eventInfo{nil, nil}

	yamlText, err := formatObjects(objs, events, "yaml")
	require.NoError(t, err)
	require.Contains(t, yamlText, "\n---\n")

	jsonText, err := formatObjects(objs, events, "json")
	require.NoError(t, err)
	require.True(t, jsonText[0] == '[', "multiple json objects render as an array")
}

func Test_briefObject_labelsAndAnnotations(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name":              "cfg",
			"namespace":         "web",
			"creationTimestamp": "2026-07-20T09:00:00Z",
			"labels":            map[string]any{"app": "api", "tier": "backend"},
			"annotations": map[string]any{
				"note": "hello",
				"kubectl.kubernetes.io/last-applied-configuration": "{...}",
			},
		},
	}}

	text := briefObject(obj, nil)
	require.Contains(t, text, "Labels: app=api, tier=backend")
	require.Contains(t, text, "Annotations: note=hello")
	// The noisy last-applied annotation is dropped.
	require.NotContains(t, text, "last-applied-configuration")
}

func Test_briefObject_noMetadata(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata":   map[string]any{"name": "web"},
	}}
	text := briefObject(obj, nil)
	require.Contains(t, text, "Labels: <none>")
	require.Contains(t, text, "Annotations: <none>")
}

func Test_briefObject_defaultsEmptyEventType(t *testing.T) {
	text := briefObject(newPod("web", "api"), []eventInfo{{Reason: "Scheduled", Message: "assigned"}})
	require.Contains(t, text, "Normal Scheduled: assigned")
}

func Test_formatList(t *testing.T) {
	c := newTestCluster()
	mapping, err := c.mappingFor("pods")
	require.NoError(t, err)

	t.Run("empty", func(t *testing.T) {
		list := &unstructured.UnstructuredList{}
		require.Equal(t, "No pods found.", formatList(list, mapping, false))
	})

	t.Run("single namespace with status", func(t *testing.T) {
		list := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*newPod("web", "api")}}
		text := formatList(list, mapping, false)
		require.Contains(t, text, "NAME")
		require.Contains(t, text, "STATUS")
		require.NotContains(t, text, "NAMESPACE")
		require.Contains(t, text, "Running")
	})

	t.Run("all namespaces shows namespace column", func(t *testing.T) {
		list := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*newPod("web", "api"), *newPod("db", "cache")}}
		text := formatList(list, mapping, true)
		require.Contains(t, text, "NAMESPACE")
	})

	t.Run("pod ready and restarts columns", func(t *testing.T) {
		pod := podWith(map[string]any{"ready": true}, map[string]any{"ready": false, "restartCount": int64(4)})
		list := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*pod}}
		text := formatList(list, mapping, false)
		require.Contains(t, text, "READY")
		require.Contains(t, text, "RESTARTS")
		require.Contains(t, text, "1/2")
		require.Contains(t, text, "4")
	})

	t.Run("deployment ready and replica columns", func(t *testing.T) {
		deploy := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata":   map[string]any{"name": "web", "namespace": "app"},
			"spec":       map[string]any{"replicas": int64(3)},
			"status":     map[string]any{"readyReplicas": int64(1), "updatedReplicas": int64(2), "availableReplicas": int64(1)},
		}}
		list := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*deploy}}
		text := formatList(list, deploymentMapping(), false)
		require.Contains(t, text, "UP-TO-DATE")
		require.Contains(t, text, "AVAILABLE")
		require.Contains(t, text, "1/3")
	})

	t.Run("crd health surfaces in status column", func(t *testing.T) {
		app := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Application",
			"metadata":   map[string]any{"name": "web", "namespace": "argocd"},
			"status":     map[string]any{"health": map[string]any{"status": "Degraded"}},
		}}
		appMapping := &meta.RESTMapping{
			Resource:         schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"},
			GroupVersionKind: schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"},
			Scope:            meta.RESTScopeNamespace,
		}
		list := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*app}}
		text := formatList(list, appMapping, false)
		require.Contains(t, text, "STATUS")
		require.Contains(t, text, "Degraded")
	})

	t.Run("no status column and rendered age", func(t *testing.T) {
		nodeMapping, err := c.mappingFor("nodes")
		require.NoError(t, err)
		node := newNode("n1")
		node.Object["metadata"].(map[string]any)["creationTimestamp"] = "2020-01-01T00:00:00Z"
		list := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*node}}
		text := formatList(list, nodeMapping, false)
		require.NotContains(t, text, "STATUS")
		require.NotContains(t, text, "<unknown>")
		require.Contains(t, text, "n1")
	})
}

func Test_humanAge(t *testing.T) {
	now := time.Now()
	testCases := map[string]struct {
		t    time.Time
		want string
	}{
		"seconds": {t: now.Add(-30 * time.Second), want: "30s"},
		"minutes": {t: now.Add(-5 * time.Minute), want: "5m"},
		"hours":   {t: now.Add(-3 * time.Hour), want: "3h"},
		"days":    {t: now.Add(-48 * time.Hour), want: "2d"},
		"future":  {t: now.Add(time.Hour), want: "0s"},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, humanAge(tc.t))
		})
	}
}

func Test_renderTable(t *testing.T) {
	rows := [][]string{{"NAME", "AGE"}, {"a", "1d"}, {"longer-name", "2h"}}
	text := renderTable(rows)
	require.Contains(t, text, "NAME         AGE")
	require.Contains(t, text, "longer-name  2h")
}
