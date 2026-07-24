package k8s

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/hangxie/chatops/tool"
)

// listTool lists the objects of one resource type in a namespace or across all
// namespaces.
type listTool struct {
	client resourceClient
}

// Invoke reads call.Arguments: kind is required; namespace and all-namespaces
// are optional. all-namespaces wins over namespace for namespaced types.
func (t *listTool) Invoke(ctx context.Context, call tool.Call) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, fmt.Errorf("k8s: %w", err)
	}
	kind := strings.TrimSpace(call.Arguments[argKind])
	if kind == "" {
		return tool.Result{}, errors.New("k8s: list requires a kind")
	}
	namespace := strings.TrimSpace(call.Arguments[argNamespace])
	allNamespaces, err := parseBool(call.Arguments[argAllNamespaces])
	if err != nil {
		return tool.Result{}, err
	}
	output, err := normalizeListOutput(call.Arguments[argOutput])
	if err != nil {
		return tool.Result{}, err
	}

	list, mapping, err := t.client.list(ctx, kind, namespace, allNamespaces)
	if err != nil {
		return tool.Result{}, err
	}
	if output != "" {
		return tool.Result{Text: marshalItems(list, output)}, nil
	}
	return tool.Result{Text: formatList(list, mapping, allNamespaces)}, nil
}

// normalizeListOutput maps the list output argument to "" (the default table),
// json, or yaml, rejecting anything else. table and brief are accepted as names
// for the default so a model may pass either.
func normalizeListOutput(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "table", outputBrief:
		return "", nil
	case outputJSON:
		return outputJSON, nil
	case outputYAML:
		return outputYAML, nil
	default:
		return "", fmt.Errorf("k8s: unknown output %q; want table, json, or yaml", raw)
	}
}

// marshalItems redacts every item and renders the list as json or yaml, so
// structured output never carries secret material into chat. A marshal failure
// is reported inline rather than as an error, since the list itself succeeded.
func marshalItems(list *unstructured.UnstructuredList, output string) string {
	objs := make([]*unstructured.Unstructured, len(list.Items))
	events := make([][]eventInfo, len(list.Items))
	for i := range list.Items {
		item := &list.Items[i]
		redact(item)
		objs[i] = item
	}
	text, err := formatObjects(objs, events, output)
	if err != nil {
		return err.Error()
	}
	return text
}

// Close releases nothing; the dynamic client owns its transport.
func (t *listTool) Close() error { return nil }

// parseBool accepts an empty string as false and otherwise the usual boolean
// spellings, so a model may pass "true"/"false" or omit the argument.
func parseBool(raw string) (bool, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "", "false", "0", "no":
		return false, nil
	case "true", "1", "yes":
		return true, nil
	default:
		return false, fmt.Errorf("k8s: invalid boolean %q", raw)
	}
}
