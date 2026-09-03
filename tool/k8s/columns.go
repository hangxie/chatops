package k8s

import (
	"strconv"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// column is one value cell in the list table, placed between NAME and AGE. Its
// value extractor returns "" when the field is absent, so a column that is
// empty for every item is dropped before rendering.
type column struct {
	name  string
	value func(obj *unstructured.Unstructured) string
}

// valueColumns picks the value columns for a resource type, mirroring the shape
// kubectl prints for the common workloads and falling back to a single status
// column that also covers CRDs (e.g. Argo CD Applications report health under
// status.health.status rather than status.phase).
func valueColumns(mapping *meta.RESTMapping) []column {
	gk := mapping.GroupVersionKind.GroupKind()
	switch {
	case gk.Group == "" && gk.Kind == "Pod":
		return []column{
			{name: "READY", value: podReady},
			{name: "STATUS", value: statusValue},
			{name: "RESTARTS", value: podRestarts},
		}
	case gk.Group == "apps" && gk.Kind == "Deployment":
		return []column{
			{name: "READY", value: deploymentReady},
			{name: "UP-TO-DATE", value: statusIntColumn("updatedReplicas")},
			{name: "AVAILABLE", value: statusIntColumn("availableReplicas")},
		}
	default:
		return []column{{name: "STATUS", value: statusValue}}
	}
}

// dropEmptyColumns removes columns whose value is empty for every item, so a
// resource type that never populates a field does not show a blank column.
func dropEmptyColumns(cols []column, list *unstructured.UnstructuredList) []column {
	kept := make([]column, 0, len(cols))
	for _, col := range cols {
		for i := range list.Items {
			if col.value(&list.Items[i]) != "" {
				kept = append(kept, col)
				break
			}
		}
	}
	return kept
}

// statusValue reports a resource's status, trying the standard phase first and
// then the health status that CRDs such as Argo CD Applications use.
func statusValue(obj *unstructured.Unstructured) string {
	if phase, found, _ := unstructured.NestedString(obj.Object, "status", "phase"); found && phase != "" {
		return phase
	}
	if health, found, _ := unstructured.NestedString(obj.Object, "status", "health", "status"); found && health != "" {
		return health
	}
	return ""
}

// podReady renders the ready/total container count, or "" when the pod carries
// no container statuses yet.
func podReady(obj *unstructured.Unstructured) string {
	statuses, found, _ := unstructured.NestedSlice(obj.Object, "status", "containerStatuses")
	if !found {
		return ""
	}
	ready := 0
	for _, s := range statuses {
		m, ok := s.(map[string]any)
		if !ok {
			continue
		}
		if r, ok := m["ready"].(bool); ok && r {
			ready++
		}
	}
	return strconv.Itoa(ready) + "/" + strconv.Itoa(len(statuses))
}

// podRestarts sums restart counts across containers, or "" when the pod carries
// no container statuses yet.
func podRestarts(obj *unstructured.Unstructured) string {
	statuses, found, _ := unstructured.NestedSlice(obj.Object, "status", "containerStatuses")
	if !found {
		return ""
	}
	var total int64
	for _, s := range statuses {
		m, ok := s.(map[string]any)
		if !ok {
			continue
		}
		switch v := m["restartCount"].(type) {
		case int64:
			total += v
		case float64:
			total += int64(v)
		}
	}
	return strconv.FormatInt(total, 10)
}

// deploymentReady renders ready replicas over the desired count, the signal for
// an under-provisioned Deployment (ready < desired).
func deploymentReady(obj *unstructured.Unstructured) string {
	ready := statusInt(obj, "status", "readyReplicas")
	desired := statusInt(obj, "spec", "replicas")
	return strconv.FormatInt(ready, 10) + "/" + strconv.FormatInt(desired, 10)
}

// statusIntColumn extracts an integer status field as a decimal string, or ""
// when the field is absent.
func statusIntColumn(field string) func(*unstructured.Unstructured) string {
	return func(obj *unstructured.Unstructured) string {
		value, found, err := unstructured.NestedInt64(obj.Object, "status", field)
		if err != nil || !found {
			return ""
		}
		return strconv.FormatInt(value, 10)
	}
}

// statusInt reads an integer at path, defaulting to 0 when absent.
func statusInt(obj *unstructured.Unstructured, path ...string) int64 {
	value, found, err := unstructured.NestedInt64(obj.Object, path...)
	if err != nil || !found {
		return 0
	}
	return value
}
