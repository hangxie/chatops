package k8s

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	// redactedValue replaces every Secret value, regardless of the original.
	redactedValue = "**REDACTED**"

	// lastAppliedAnnotation holds a serialized copy of the last applied
	// manifest. For a Secret that copy embeds the original values, so it is
	// stripped before serialization rather than masked field by field.
	lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"
)

// redact masks the values of a core Secret in place so no secret material
// reaches chat, whatever the output format. Keys are preserved so the shape of
// the Secret stays visible; only values are masked. It also drops the
// last-applied-configuration annotation, which would otherwise round-trip the
// original values through JSON and YAML output. Non-Secret objects are left
// untouched.
func redact(obj *unstructured.Unstructured) {
	gvk := obj.GroupVersionKind()
	if gvk.Group != "" || gvk.Kind != "Secret" {
		return
	}
	stripLastApplied(obj)
	for _, field := range []string{"data", "stringData"} {
		values, found, err := unstructured.NestedMap(obj.Object, field)
		if err != nil || !found {
			continue
		}
		for key := range values {
			values[key] = redactedValue
		}
		_ = unstructured.SetNestedMap(obj.Object, values, field)
	}
}

// stripLastApplied removes the last-applied-configuration annotation, dropping
// the whole annotations map when it becomes empty.
func stripLastApplied(obj *unstructured.Unstructured) {
	annotations := obj.GetAnnotations()
	if _, ok := annotations[lastAppliedAnnotation]; !ok {
		return
	}
	delete(annotations, lastAppliedAnnotation)
	if len(annotations) == 0 {
		unstructured.RemoveNestedField(obj.Object, "metadata", "annotations")
		return
	}
	obj.SetAnnotations(annotations)
}
