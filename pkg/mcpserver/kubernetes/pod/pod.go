// Package pod serves the read-only pod tools: roster, cleaned definition, logs, and resource usage.
package pod

import (
	"context"
	"errors"
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

// PodListOutput is the structured result of pod_list; Namespaces and Node echo the filters given.
type PodListOutput struct {
	Namespaces []string      `json:"namespaces,omitempty"`
	Node       string        `json:"node,omitempty"`
	Pods       []PodListItem `json:"pods"`
	// Total counts every match before the cap.
	Total int `json:"total"`
	// Cut lists the requested namespaces the cap shortened, so the model can list one alone.
	Cut []NamespaceCut `json:"cut,omitempty"`
}

// NamespaceCut is one namespace's share of a capped multi-namespace list.
type NamespaceCut struct {
	Namespace string `json:"namespace"`
	Shown     int    `json:"shown"`
	Total     int    `json:"total"`
}

type podListInput struct {
	Namespaces []string `json:"namespaces,omitempty" jsonschema:"the namespaces to list, all in one call; omit to list every namespace"`
	Node       string   `json:"node,omitempty" jsonschema:"only pods scheduled on this node"`
}

type podInput struct {
	Pods []kube.ObjectRef `json:"pods" jsonschema:"the pods to report on, all in one call"`
}

// Register adds the pod tools to server.
func Register(server *mcp.Server, c kube.Clients) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "pod_list",
		Description: "List Kubernetes pods with ready containers, status, restarts, node, age, and owning controller. Pass namespaces to list only those namespaces, all in one call (omit for every namespace), and node to list only the pods on that node.",
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
		out, err := listPods(ctx, c.Core, in.Namespaces, in.Node, c.ResultLimit())
		if err != nil {
			return nil, PodListOutput{}, err
		}
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

// listPods lists every namespace when namespaces is empty, and fails naming each requested namespace that does not exist.
func listPods(ctx context.Context, client k8s.Interface, namespaces []string, node string, limit int) (PodListOutput, error) {
	node = strings.TrimSpace(node)
	var lists []namespacePods
	if len(namespaces) == 0 {
		all, err := listNamespacePods(ctx, client, "", node)
		if err != nil {
			return PodListOutput{}, err
		}
		lists = []namespacePods{all}
	} else {
		var err error
		// No cap: max_results bounds the rows, and a list too long to fetch in time fails as a timeout the model can narrow.
		if namespaces, err = kube.CappedTargets("namespaces", namespaces, len(namespaces)); err != nil {
			return PodListOutput{}, err
		}
		fetch := func(ctx context.Context, namespace string) (namespacePods, error) {
			return listNamespacePods(ctx, client, namespace, node)
		}
		if lists, err = kube.FetchAll(ctx, namespaces, fetch); err != nil {
			return PodListOutput{}, err
		}
	}
	out := PodListOutput{Namespaces: namespaces, Node: node, Pods: []PodListItem{}}
	var missing []string
	sizes := make([]int, len(lists))
	for i, list := range lists {
		if list.missing != "" {
			missing = append(missing, list.missing)
		}
		sizes[i] = len(list.pods)
		out.Total += sizes[i]
	}
	if len(missing) > 0 {
		return PodListOutput{}, errors.New(strings.Join(missing, "; "))
	}
	// An even split keeps one large namespace from crowding out the others the caller asked for.
	for i, share := range kube.FairShares(sizes, limit) {
		out.Pods = append(out.Pods, lists[i].pods[:share]...)
		if share < sizes[i] && len(lists) > 1 {
			out.Cut = append(out.Cut, NamespaceCut{Namespace: namespaces[i], Shown: share, Total: sizes[i]})
		}
	}
	return out, nil
}

// namespacePods is one namespace's pods, or a note when the namespace does not exist.
type namespacePods struct {
	pods    []PodListItem
	missing string
}

func listNamespacePods(ctx context.Context, client k8s.Interface, namespace, node string) (namespacePods, error) {
	opts := metav1.ListOptions{}
	if node != "" {
		opts.FieldSelector = fields.OneTermEqualSelector("spec.nodeName", node).String()
	}
	list, err := client.CoreV1().Pods(namespace).List(ctx, opts)
	if err != nil {
		return namespacePods{}, err
	}
	if len(list.Items) == 0 && namespace != "" {
		missing, err := kube.MissingNamespace(ctx, client, namespace)
		return namespacePods{missing: missing}, err
	}
	pods := make([]PodListItem, 0, len(list.Items))
	for i := range list.Items {
		p := &list.Items[i]
		s := kube.SummarizePod(p)
		pods = append(pods, PodListItem{
			Namespace: p.Namespace, Name: p.Name, Ready: s.Ready, Containers: s.Containers, Status: s.Status,
			Restarts: s.Restarts, Node: s.Node, Owner: owner(p), Created: s.Created,
		})
	}
	return namespacePods{pods: pods}, nil
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
