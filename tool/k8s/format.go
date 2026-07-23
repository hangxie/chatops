package k8s

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// output formats a get tool understands.
const (
	outputBrief = "brief"
	outputJSON  = "json"
	outputYAML  = "yaml"
)

// skippedAnnotations are noisy machine annotations dropped from the brief view.
var skippedAnnotations = map[string]bool{
	lastAppliedAnnotation: true,
}

// validateOutput rejects an unknown output format before any API call.
func validateOutput(output string) error {
	switch output {
	case outputBrief, outputJSON, outputYAML, "":
		return nil
	default:
		return fmt.Errorf("k8s: unknown output %q; want brief, json, or yaml", output)
	}
}

// formatObjects renders one or more objects in the requested output. brief is
// a describe-style summary (events supplied per object); json and yaml emit the
// full manifests. Multiple objects are separated so each stays readable.
func formatObjects(objs []*unstructured.Unstructured, events [][]eventInfo, output string) (string, error) {
	switch output {
	case outputJSON:
		return marshalJSON(objs)
	case outputYAML:
		return marshalYAML(objs)
	case outputBrief, "":
		parts := make([]string, len(objs))
		for i, obj := range objs {
			parts[i] = briefObject(obj, events[i])
		}
		return strings.Join(parts, "\n\n"), nil
	default:
		return "", fmt.Errorf("k8s: unknown output %q; want brief, json, or yaml", output)
	}
}

func marshalJSON(objs []*unstructured.Unstructured) (string, error) {
	var value any
	if len(objs) == 1 {
		value = objs[0].Object
	} else {
		items := make([]any, len(objs))
		for i, obj := range objs {
			items[i] = obj.Object
		}
		value = items
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", fmt.Errorf("k8s: encode json: %w", err)
	}
	return string(data), nil
}

func marshalYAML(objs []*unstructured.Unstructured) (string, error) {
	parts := make([]string, len(objs))
	for i, obj := range objs {
		data, err := yaml.Marshal(obj.Object)
		if err != nil {
			return "", fmt.Errorf("k8s: encode yaml: %w", err)
		}
		parts[i] = strings.TrimRight(string(data), "\n")
	}
	return strings.Join(parts, "\n---\n"), nil
}

// briefObject renders a describe-style summary: identity, age, labels, a status
// hint, and recent events.
func briefObject(obj *unstructured.Unstructured, events []eventInfo) string {
	var b strings.Builder
	gvk := obj.GroupVersionKind()
	fmt.Fprintf(&b, "%s: %s\n", gvk.Kind, obj.GetName())
	if ns := obj.GetNamespace(); ns != "" {
		fmt.Fprintf(&b, "Namespace: %s\n", ns)
	}
	fmt.Fprintf(&b, "API Version: %s\n", obj.GetAPIVersion())
	if ts := obj.GetCreationTimestamp(); !ts.IsZero() {
		fmt.Fprintf(&b, "Age: %s\n", humanAge(ts.Time))
	}
	writeMap(&b, "Labels", obj.GetLabels(), nil)
	writeMap(&b, "Annotations", obj.GetAnnotations(), skippedAnnotations)
	if phase, found, _ := unstructured.NestedString(obj.Object, "status", "phase"); found && phase != "" {
		fmt.Fprintf(&b, "Status: %s\n", phase)
	}
	writeEvents(&b, events)
	return strings.TrimRight(b.String(), "\n")
}

func writeMap(b *strings.Builder, label string, entries map[string]string, skip map[string]bool) {
	keys := make([]string, 0, len(entries))
	for key := range entries {
		if skip[key] {
			continue
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		fmt.Fprintf(b, "%s: <none>\n", label)
		return
	}
	sort.Strings(keys)
	pairs := make([]string, len(keys))
	for i, key := range keys {
		pairs[i] = key + "=" + entries[key]
	}
	fmt.Fprintf(b, "%s: %s\n", label, strings.Join(pairs, ", "))
}

func writeEvents(b *strings.Builder, events []eventInfo) {
	if len(events) == 0 {
		return
	}
	b.WriteString("Events:\n")
	for _, event := range events {
		kind := event.Type
		if kind == "" {
			kind = "Normal"
		}
		fmt.Fprintf(b, "  %s %s: %s\n", kind, event.Reason, event.Message)
	}
}

// formatList renders a list as an aligned table. Columns are NAME and AGE, with
// NAMESPACE prepended across namespaces and STATUS appended when any item
// carries a status phase, so the view works for any resource type including
// CRDs.
func formatList(list *unstructured.UnstructuredList, mapping *meta.RESTMapping, allNamespaces bool) string {
	kind := mapping.Resource.Resource
	if len(list.Items) == 0 {
		return "No " + kind + " found."
	}

	namespaced := mapping.Scope.Name() == meta.RESTScopeNameNamespace
	showNamespace := namespaced && allNamespaces
	showStatus := listHasStatus(list)

	header := []string{}
	if showNamespace {
		header = append(header, "NAMESPACE")
	}
	header = append(header, "NAME", "AGE")
	if showStatus {
		header = append(header, "STATUS")
	}

	rows := [][]string{header}
	for i := range list.Items {
		item := &list.Items[i]
		row := []string{}
		if showNamespace {
			row = append(row, item.GetNamespace())
		}
		row = append(row, item.GetName(), itemAge(item))
		if showStatus {
			phase, _, _ := unstructured.NestedString(item.Object, "status", "phase")
			row = append(row, phase)
		}
		rows = append(rows, row)
	}
	return renderTable(rows)
}

func listHasStatus(list *unstructured.UnstructuredList) bool {
	for i := range list.Items {
		if phase, found, _ := unstructured.NestedString(list.Items[i].Object, "status", "phase"); found && phase != "" {
			return true
		}
	}
	return false
}

func itemAge(obj *unstructured.Unstructured) string {
	ts := obj.GetCreationTimestamp()
	if ts.IsZero() {
		return "<unknown>"
	}
	return humanAge(ts.Time)
}

// renderTable left-aligns columns padded to the widest cell.
func renderTable(rows [][]string) string {
	widths := []int{}
	for _, row := range rows {
		for col, cell := range row {
			if col >= len(widths) {
				widths = append(widths, 0)
			}
			widths[col] = max(widths[col], len(cell))
		}
	}
	lines := make([]string, len(rows))
	for i, row := range rows {
		cells := make([]string, len(row))
		for col, cell := range row {
			if col == len(row)-1 {
				cells[col] = cell
			} else {
				cells[col] = fmt.Sprintf("%-*s", widths[col], cell)
			}
		}
		lines[i] = strings.TrimRight(strings.Join(cells, "  "), " ")
	}
	return strings.Join(lines, "\n")
}

// humanAge renders a duration the way kubectl does: the two most significant
// units, collapsing to one for long and short ages.
func humanAge(t time.Time) string {
	d := time.Since(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	}
}
