package k8s

import (
	"context"
	"errors"
	"fmt"
	"strings"

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

	list, mapping, err := t.client.list(ctx, kind, namespace, allNamespaces)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Text: formatList(list, mapping, allNamespaces)}, nil
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
