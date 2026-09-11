package k8s

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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
		obj, mapping, err := client.get(ctx, kind, namespace, name)
		if err != nil {
			return nil, nil, notFound(err, kind, name, namespace, mapping)
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

// notFound turns "no such object" into a message built from what the
// requester asked for.
//
// The error client-go raises carries the API server's own phrasing and, on
// the way out, its address; the requester mistyped a name and needs to be
// told that, not sent to check whether the cluster is up.
//
// Where it looked is only stated when it can be stated correctly. A
// cluster-scoped type has no namespace, so naming one would be wrong even if
// the requester supplied it; and a namespaced lookup with no namespace given
// used the context's default, which is not necessarily the namespace called
// "default". Without the mapping the scope is unknown, so nothing is claimed.
func notFound(err error, kind, name, namespace string, mapping *meta.RESTMapping) error {
	if !apierrors.IsNotFound(err) {
		return err
	}
	if mapping == nil || mapping.Scope.Name() != meta.RESTScopeNameNamespace {
		return invalidCall("k8s: no %s named %q", kind, name)
	}
	if namespace == "" {
		return invalidCall("k8s: no %s named %q in this context's default namespace", kind, name)
	}
	return invalidCall("k8s: no %s named %q in namespace %q", kind, name, namespace)
}
