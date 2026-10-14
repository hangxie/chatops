// Package pod serves the read-only pod tools: roster, cleaned definition, logs, and resource usage.
package pod

import (
	"context"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	k8s "k8s.io/client-go/kubernetes"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

// PodListItem is one pod's roster entry; Owner is its controller as Kind/name.
type PodListItem struct {
	Namespace  string    `json:"namespace"`
	Name       string    `json:"name"`
	Ready      int       `json:"ready"`
	Containers int       `json:"containers"`
	Status     string    `json:"status"`
	Restarts   int32     `json:"restarts"`
	Node       string    `json:"node,omitempty"`
	Owner      string    `json:"owner,omitempty"`
	Created    time.Time `json:"created"`
}

// PodListOutput is the structured result of pod_list; Namespace and Node echo the filters given.
type PodListOutput struct {
	Namespace string        `json:"namespace,omitempty"`
	Node      string        `json:"node,omitempty"`
	Pods      []PodListItem `json:"pods"`
	// Total counts every match before the cap.
	Total int `json:"total"`
}

type podListInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"the namespace to list; omit to list every namespace"`
	Node      string `json:"node,omitempty" jsonschema:"only pods scheduled on this node"`
}

type podInput struct {
	Pods []kube.ObjectRef `json:"pods" jsonschema:"the pods to report on, all in one call"`
}

// Register adds the pod tools to server.
func Register(server *mcp.Server, c kube.Clients) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "pod_list",
		Description: "List Kubernetes pods with ready containers, status, restarts, node, age, and owning controller. Pass namespace to list one namespace (omit for every namespace) and node to list only the pods on that node.",
		Annotations: kube.ReadOnly(),
	}, podListHandler(c))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "pod_describe",
		Description: "Describe up to 5 Kubernetes pods in one call: status plus the full pod object as YAML (spec, container statuses, conditions), with literal environment values redacted. Pass pods as {namespace, name}; a pod that does not exist has found false.",
		Annotations: kube.ReadOnly(),
	}, podDescribeHandler(c))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "pod_log",
		Description: "Show the recent log output of up to 5 Kubernetes pods in one call, every main container unless container is given. tail_lines sets how many lines (default 50, at most 500); previous true shows the last crashed instance, which explains a CrashLoopBackOff. Pass pods as {namespace, name}.",
		Annotations: kube.ReadOnly(),
	}, podLogHandler(c))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "pod_resources",
		Description: "Report CPU and memory requests, limits, and current usage per container for one or more Kubernetes pods in one call. Pass pods as {namespace, name}; a pod that does not exist has found false.",
		Annotations: kube.ReadOnly(),
	}, podResourcesHandler(c))
}

func podListHandler(c kube.Clients) mcp.ToolHandlerFor[podListInput, PodListOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in podListInput) (*mcp.CallToolResult, PodListOutput, error) {
		out, err := listPods(ctx, c.Core, in.Namespace, in.Node)
		if err != nil {
			return nil, PodListOutput{}, err
		}
		out.Pods, out.Total = kube.Cap(out.Pods, c.ResultLimit())
		return kube.TextResult(formatPodList(out)), out, nil
	}
}

func podDescribeHandler(c kube.Clients) mcp.ToolHandlerFor[podInput, PodDescribeOutput] {
	return kube.PerTarget(
		func(in podInput) ([]kube.ObjectRef, error) {
			return kube.ObjectTargets("pods", in.Pods, kube.MaxDescribeTargets)
		},
		func(ctx context.Context, ref kube.ObjectRef) (kube.Described, error) {
			return describePod(ctx, c.Core, ref)
		},
		kube.FormatDescribed("pod"),
		func(p []kube.Described) PodDescribeOutput { return PodDescribeOutput{Pods: p} },
	)
}

func listPods(ctx context.Context, client k8s.Interface, namespace, node string) (PodListOutput, error) {
	namespace, node = strings.TrimSpace(namespace), strings.TrimSpace(node)
	opts := metav1.ListOptions{}
	if node != "" {
		opts.FieldSelector = fields.OneTermEqualSelector("spec.nodeName", node).String()
	}
	list, err := client.CoreV1().Pods(namespace).List(ctx, opts)
	if err != nil {
		return PodListOutput{}, err
	}
	if err := kube.CheckNamespace(ctx, client, namespace, len(list.Items)); err != nil {
		return PodListOutput{}, err
	}
	out := PodListOutput{Namespace: namespace, Node: node, Pods: make([]PodListItem, 0, len(list.Items))}
	for i := range list.Items {
		p := &list.Items[i]
		s := kube.SummarizePod(p)
		out.Pods = append(out.Pods, PodListItem{
			Namespace: p.Namespace, Name: p.Name, Ready: s.Ready, Containers: s.Containers, Status: s.Status,
			Restarts: s.Restarts, Node: s.Node, Owner: owner(p), Created: s.Created,
		})
	}
	return out, nil
}

func owner(p *corev1.Pod) string {
	ref := metav1.GetControllerOf(p)
	if ref == nil {
		return ""
	}
	return ref.Kind + "/" + ref.Name
}

// getPod returns nil and a note on a miss; the note names the namespace when that is what is missing.
func getPod(ctx context.Context, client k8s.Interface, ref kube.ObjectRef) (*corev1.Pod, string, error) {
	p, found, note, err := kube.GetNamespaced(ctx, client, ref, client.CoreV1().Pods(ref.Namespace).Get)
	if !found {
		return nil, note, err
	}
	return p, "", nil
}
