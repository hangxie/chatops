// Package k8s reads Kubernetes resources for chat. It resolves resource types
// through the API server's discovery data and reads objects with the dynamic
// client, so it serves built-in resources and CustomResourceDefinitions alike.
//
// The package exports two single-intent tools for wiring into a tool.Registry:
//
//	k8s-list  lists a resource type in a namespace or across all namespaces
//	k8s-get   fetches specific resources as a brief, JSON, or YAML
//
// # Cluster selection
//
// Credentials never appear in the tool URL. How to reach a cluster — API server
// URL, CA bundle, and client certificate or token — comes from a kubeconfig
// loaded through the standard rules (the KUBECONFIG environment variable, then
// ~/.kube/config), falling back to the in-cluster service account when running
// in a pod. Configuring KUBECONFIG once therefore serves every k8s tool. The
// URL only names which cluster and defaults to apply:
//
//	k8s-get://                             current context (or in-cluster)
//	k8s-get://?context=prod                a named kubeconfig context
//	k8s-get://?kubeconfig=/path/to/config  an explicit kubeconfig file
//
// # Secret safety
//
// Secret values are always masked before rendering, in every output format, so
// a describe or manifest never carries secret material into chat.
package k8s

import (
	"context"
	"fmt"
	"net/url"

	"github.com/hangxie/chatops/cred"
	"github.com/hangxie/chatops/tool"
)

// Schemes served in a tool.Registry.
const (
	ListScheme = "k8s-list"
	GetScheme  = "k8s-get"
)

// Argument keys the tools read from tool.Call.Arguments.
const (
	argKind          = "kind"
	argName          = "name"
	argNamespace     = "namespace"
	argAllNamespaces = "all-namespaces"
	argOutput        = "output"
)

// URL query keys carrying cluster selection (operator configuration, not
// model-facing arguments).
const (
	queryContext    = "context"
	queryKubeconfig = "kubeconfig"
)

// ListDescriptor and GetDescriptor are the tools' self-descriptions for
// planners; wire each into a tool.Backend alongside its scheme and opener.
var (
	ListDescriptor = tool.Descriptor{
		Description: "List Kubernetes resources of one type in a namespace or across all namespaces (pods, deployments, CRDs, ...).",
		Parameters: []tool.Param{
			{Name: argKind, Type: "string", Required: true, Description: "Resource type: plural, singular, short name, or kind (e.g. pods, po, deployment, StatefulSet, CRD names)."},
			{Name: argNamespace, Type: "string", Description: "Namespace to list; defaults to the context's default namespace. Ignored for cluster-scoped types."},
			{Name: argAllNamespaces, Type: "boolean", Description: "List across all namespaces instead of one."},
		},
	}
	GetDescriptor = tool.Descriptor{
		Description: "Fetch specific Kubernetes resources by name as a describe-style brief, JSON, or YAML. Secret values are masked.",
		Parameters: []tool.Param{
			{Name: argKind, Type: "string", Required: true, Description: "Resource type: plural, singular, short name, or kind (e.g. pod, statefulset, CRD names)."},
			{Name: argName, Type: "string", Required: true, Description: "Resource name; pass several as a comma-separated list to fetch them together."},
			{Name: argNamespace, Type: "string", Description: "Namespace of the resource; defaults to the context's default namespace. Ignored for cluster-scoped types."},
			{Name: argOutput, Type: "string", Description: "Output format: brief (default, a summary with recent events), json, or yaml."},
		},
	}
)

// ListOpener and GetOpener are the tool.OpenerFunc values for the two tools.
// creds is unused: cluster access comes from the kubeconfig or in-cluster
// config selected by the URL, never from the credential store.
func ListOpener(ctx context.Context, u *url.URL, _ cred.Store) (tool.Tool, error) {
	client, err := openCluster(u)
	if err != nil {
		return nil, err
	}
	return &listTool{client: client}, nil
}

func GetOpener(ctx context.Context, u *url.URL, _ cred.Store) (tool.Tool, error) {
	client, err := openCluster(u)
	if err != nil {
		return nil, err
	}
	return &getTool{client: client}, nil
}

// openCluster parses the cluster selection from u and builds a cluster client.
func openCluster(u *url.URL) (*cluster, error) {
	cc, err := parseURL(u)
	if err != nil {
		return nil, err
	}
	return newCluster(cc)
}

// parseURL reads cluster selection from the tool URL. The URL carries no host:
// the API server address lives in the kubeconfig, so a stray host is rejected
// to catch a kubeconfig path or context mistakenly placed there.
func parseURL(u *url.URL) (clusterConfig, error) {
	if u.Host != "" || u.Opaque != "" || u.User != nil {
		return clusterConfig{}, fmt.Errorf("k8s: URL %q takes no host; select a cluster with ?context= or ?kubeconfig=", u.String())
	}
	query := u.Query()
	for key := range query {
		switch key {
		case queryContext, queryKubeconfig:
		default:
			return clusterConfig{}, fmt.Errorf("k8s: unknown URL parameter %q", key)
		}
	}
	return clusterConfig{
		kubeconfig: query.Get(queryKubeconfig),
		context:    query.Get(queryContext),
	}, nil
}
