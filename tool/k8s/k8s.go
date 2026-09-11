// Package k8s reads Kubernetes resources for chat and exposes them as MCP
// tools.
//
// The package exports GroupName and Register for wiring its tools into an
// mcpserve.Registry. The group registers two single-intent tools:
//
//	k8s-list  lists a resource type in a namespace or across all namespaces
//	k8s-get   fetches specific resources as a brief, JSON, or YAML
//
// # Cluster selection
//
// Credentials never appear in the group's options. How to reach a cluster —
// API server URL, CA bundle, and client certificate or token — comes from a
// kubeconfig loaded through the standard rules (the KUBECONFIG environment
// variable, then ~/.kube/config), falling back to the in-cluster service
// account when running in a pod. Configuring KUBECONFIG once therefore serves
// the whole group. The options only name which cluster and defaults to apply,
// and are shared by both tools:
//
//	k8s                             current context (or in-cluster)
//	k8s?context=prod                a named kubeconfig context
//	k8s?kubeconfig=/path/to/config  an explicit kubeconfig file
//
// # Secret safety
//
// Secret values are always masked before rendering, in every output format, so
// a describe or manifest never carries secret material into chat.
package k8s

import (
	"context"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/hangxie/chatops/mcpserve"
)

// invalidCall marks an error as caused by the call itself — a missing
// argument, an unknown resource type, a name that does not exist — and so
// safe to show the requester, which is the whole point of reporting it.
func invalidCall(format string, args ...any) error {
	return mcpserve.UserError(format, args...)
}

// notice is what the requester is told when a failure cannot be shown. It
// says the call did not get through without saying what it was talking to.
const notice = "k8s: the cluster could not be reached or the request was refused; see the server log"

// GroupName is the built-in group this package registers into.
const GroupName = "k8s"

// Model-facing tool names.
const (
	ListToolName = "k8s-list"
	GetToolName  = "k8s-get"
)

// Option keys carrying cluster selection (operator configuration, not
// model-facing arguments).
const (
	optionContext    = "context"
	optionKubeconfig = "kubeconfig"
)

// ListArgs is the input schema of the k8s-list tool.
type ListArgs struct {
	Kind          string `json:"kind" jsonschema:"Resource type: plural, singular, short name, or kind (e.g. pods, po, deployment, StatefulSet, CRD names)."`
	Namespace     string `json:"namespace,omitempty" jsonschema:"Namespace to list; defaults to the context's default namespace. Ignored for cluster-scoped types."`
	AllNamespaces bool   `json:"all-namespaces,omitempty" jsonschema:"List across all namespaces instead of one."`
}

// GetArgs is the input schema of the k8s-get tool.
type GetArgs struct {
	Kind      string `json:"kind" jsonschema:"Resource type: plural, singular, short name, or kind (e.g. pod, statefulset, CRD names)."`
	Name      string `json:"name" jsonschema:"Resource name; pass several as a comma-separated list to fetch them together."`
	Namespace string `json:"namespace,omitempty" jsonschema:"Namespace of the resource; defaults to the context's default namespace. Ignored for cluster-scoped types."`
	Output    string `json:"output,omitempty" jsonschema:"Output format: brief (default, a summary with recent events), json, or yaml."`
}

// Register adds the kubernetes tools to s, both sharing the cluster selected
// by opts. creds is unused: cluster access comes from the kubeconfig or
// in-cluster config, never from the credential store.
//
// Options are validated here, because a misspelled one is an operator mistake
// worth catching at startup. Reaching the cluster is not: the kubeconfig is
// loaded on first use, so a host without one — or with a broken one — still
// starts and simply reports the problem when a kubernetes tool is called. See
// lazyCluster.
func Register(s *mcp.Server, opts mcpserve.Options) error {
	if err := mcpserve.CheckOptions(GroupName, opts.Query, optionContext, optionKubeconfig); err != nil {
		return err
	}
	return RegisterClient(s, newLazyCluster(clusterConfig{
		kubeconfig: opts.Query.Get(optionKubeconfig),
		context:    opts.Query.Get(optionContext),
	}), opts.Logger)
}

// RegisterClient adds the kubernetes tools to s backed by client, for
// explicit wiring and tests. A nil logger discards the operational detail the
// tools keep out of their results.
func RegisterClient(s *mcp.Server, client resourceClient, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        ListToolName,
		Description: "List Kubernetes resources of one type in a namespace or across all namespaces (pods, deployments, CRDs, ...).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args ListArgs) (*mcp.CallToolResult, any, error) {
		result, out, err := listResources(ctx, client, args)
		return result, out, curate(logger, ListToolName, err)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        GetToolName,
		Description: "Fetch specific Kubernetes resources by name as a describe-style brief, JSON, or YAML. Secret values are masked.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args GetArgs) (*mcp.CallToolResult, any, error) {
		result, out, err := getResources(ctx, client, args)
		return result, out, curate(logger, GetToolName, err)
	})
	return nil
}

// curate keeps cluster-side failures out of the requester's view, while
// letting through the ones they can act on.
//
// A refusal is recognised here rather than left to the generic branch: being
// told the cluster might be unreachable, when the real answer is that this
// bot may not read Secrets, sends someone to look in the wrong place. The
// identity that was refused stays in the log.
func curate(logger *slog.Logger, tool string, err error) error {
	if apierrors.IsForbidden(err) {
		if logger != nil {
			logger.Warn("kubernetes call refused", "group", GroupName, "tool", tool, "error", err.Error())
		}
		return invalidCall("k8s: not permitted to read that resource")
	}
	return mcpserve.Curate(logger, GroupName, tool, notice, err)
}

// textResult wraps rendered output as a tool result.
func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}
