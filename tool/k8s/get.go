package k8s

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// getResources fetches specific resources by name and renders them as a
// describe-style brief, JSON, or YAML. Secret values are always masked.
func getResources(ctx context.Context, client resourceClient, args GetArgs) (*mcp.CallToolResult, any, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, fmt.Errorf("k8s: %w", err)
	}
	kind := strings.TrimSpace(args.Kind)
	if kind == "" {
		return nil, nil, invalidCall("k8s: get requires a kind")
	}
	names := splitNames(args.Name)
	if len(names) == 0 {
		return nil, nil, invalidCall("k8s: get requires a name")
	}
	output := strings.ToLower(strings.TrimSpace(args.Output))
	if err := validateOutput(output); err != nil {
		return nil, nil, err
	}
	namespace := strings.TrimSpace(args.Namespace)

	objs := make([]*unstructured.Unstructured, 0, len(names))
	events := make([][]eventInfo, 0, len(names))
	for _, name := range names {
		obj, _, err := client.get(ctx, kind, namespace, name)
		if err != nil {
			return nil, nil, err
		}
		redact(obj)
		objs = append(objs, obj)
		if output == outputBrief || output == "" {
			events = append(events, client.events(ctx, obj))
		} else {
			events = append(events, nil)
		}
	}

	text, err := formatObjects(objs, events, output)
	if err != nil {
		return nil, nil, err
	}
	return textResult(text), nil, nil
}

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
