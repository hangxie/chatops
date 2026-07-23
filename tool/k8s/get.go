package k8s

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/hangxie/chatops/tool"
)

// getTool fetches specific resources by name and renders them as a
// describe-style brief, JSON, or YAML. Secret values are always masked.
type getTool struct {
	client resourceClient
}

// Invoke reads call.Arguments: kind and name are required, name may be a
// comma-separated list; namespace and output are optional.
func (t *getTool) Invoke(ctx context.Context, call tool.Call) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, fmt.Errorf("k8s: %w", err)
	}
	kind := strings.TrimSpace(call.Arguments[argKind])
	if kind == "" {
		return tool.Result{}, errors.New("k8s: get requires a kind")
	}
	names := splitNames(call.Arguments[argName])
	if len(names) == 0 {
		return tool.Result{}, errors.New("k8s: get requires a name")
	}
	output := strings.ToLower(strings.TrimSpace(call.Arguments[argOutput]))
	if err := validateOutput(output); err != nil {
		return tool.Result{}, err
	}
	namespace := strings.TrimSpace(call.Arguments[argNamespace])

	objs := make([]*unstructured.Unstructured, 0, len(names))
	events := make([][]eventInfo, 0, len(names))
	for _, name := range names {
		obj, _, err := t.client.get(ctx, kind, namespace, name)
		if err != nil {
			return tool.Result{}, err
		}
		redact(obj)
		objs = append(objs, obj)
		if output == outputBrief || output == "" {
			events = append(events, t.client.events(ctx, obj))
		} else {
			events = append(events, nil)
		}
	}

	text, err := formatObjects(objs, events, output)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Text: text}, nil
}

// Close releases nothing; the dynamic client owns its transport.
func (t *getTool) Close() error { return nil }

// splitNames parses a comma-separated name list, trimming blanks.
func splitNames(raw string) []string {
	names := make([]string, 0)
	for part := range strings.SplitSeq(raw, ",") {
		if name := strings.TrimSpace(part); name != "" {
			names = append(names, name)
		}
	}
	return names
}
