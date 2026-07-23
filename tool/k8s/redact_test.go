package k8s

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func Test_redact(t *testing.T) {
	t.Run("masks secret data and stringData", func(t *testing.T) {
		secret := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata":   map[string]any{"name": "db"},
			"data":       map[string]any{"password": "c2VjcmV0", "user": "YWRtaW4="},
			"stringData": map[string]any{"token": "plaintext"},
		}}
		redact(secret)

		data, _, _ := unstructured.NestedMap(secret.Object, "data")
		require.Equal(t, redactedValue, data["password"])
		require.Equal(t, redactedValue, data["user"])
		stringData, _, _ := unstructured.NestedMap(secret.Object, "stringData")
		require.Equal(t, redactedValue, stringData["token"])
	})

	t.Run("strips last-applied annotation carrying secret values", func(t *testing.T) {
		raw := "c3VwZXItc2VjcmV0"
		secret := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata": map[string]any{
				"name": "db",
				"annotations": map[string]any{
					lastAppliedAnnotation: `{"kind":"Secret","data":{"password":"` + raw + `"}}`,
					"keep":                "me",
				},
			},
			"data": map[string]any{"password": raw},
		}}
		redact(secret)

		annotations := secret.GetAnnotations()
		_, present := annotations[lastAppliedAnnotation]
		require.False(t, present)
		require.Equal(t, "me", annotations["keep"])

		// Round-tripping through YAML must not surface the raw value anywhere.
		out, err := formatObjects([]*unstructured.Unstructured{secret}, [][]eventInfo{nil}, outputYAML)
		require.NoError(t, err)
		require.NotContains(t, out, raw)
	})

	t.Run("drops the annotations map when only last-applied was present", func(t *testing.T) {
		secret := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata": map[string]any{
				"name":        "db",
				"annotations": map[string]any{lastAppliedAnnotation: "{}"},
			},
		}}
		redact(secret)

		_, found, err := unstructured.NestedMap(secret.Object, "metadata", "annotations")
		require.NoError(t, err)
		require.False(t, found)
	})

	t.Run("leaves non-secret untouched", func(t *testing.T) {
		pod := newPod("web", "api")
		redact(pod)
		require.Equal(t, "Running", pod.Object["status"].(map[string]any)["phase"])
	})

	t.Run("ignores a same-kind CRD in another group", func(t *testing.T) {
		obj := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "example.com/v1",
			"kind":       "Secret",
			"data":       map[string]any{"k": "v"},
		}}
		redact(obj)
		data, _, _ := unstructured.NestedMap(obj.Object, "data")
		require.Equal(t, "v", data["k"])
	})
}
