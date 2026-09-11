package k8s

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// listResources lists the objects of one resource type in a namespace or
// across all namespaces. all-namespaces wins over namespace for namespaced
// types.
func listResources(ctx context.Context, client resourceClient, args ListArgs) (*mcp.CallToolResult, any, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, fmt.Errorf("k8s: %w", err)
	}
	kind := strings.TrimSpace(args.Kind)
	if kind == "" {
		return nil, nil, invalidCall("k8s: list requires a kind")
	}
	namespace := strings.TrimSpace(args.Namespace)

	list, mapping, err := client.list(ctx, kind, namespace, args.AllNamespaces)
	if err != nil {
		return nil, nil, err
	}
	return textResult(formatList(list, mapping, args.AllNamespaces)), nil, nil
}
