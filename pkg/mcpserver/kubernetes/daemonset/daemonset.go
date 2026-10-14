// Package daemonset serves the read-only daemonset tools: health, owned pods, and cleaned definition.
package daemonset

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8s "k8s.io/client-go/kubernetes"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

// Health words; see docs/mcp-k8s.md for the rules.
const (
	healthNoNodes     = "NoNodes"
	healthProgressing = "Progressing"
	healthDegraded    = "Degraded"
	healthHealthy     = "Healthy"
)

// DaemonSetListOutput is the structured result of daemonset_list; Namespace is empty when listing all.
type DaemonSetListOutput struct {
	Namespace  string                  `json:"namespace,omitempty"`
	DaemonSets []kube.WorkloadListItem `json:"daemonsets"`
}

type listInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"the namespace to list; omit to list every namespace"`
}

type targetInput struct {
	DaemonSets []kube.ObjectRef `json:"daemonsets" jsonschema:"the daemonsets to report on, all in one call"`
}

// Register adds the daemonset tools to server.
func Register(server *mcp.Server, c kube.Clients) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "daemonset_list",
		Description: "List Kubernetes daemonsets with their health (Healthy, Progressing, Degraded, NoNodes) and ready, up-to-date, and available pods against the nodes that should run one. Pass namespace to list one namespace; omit it to list every namespace.",
		Annotations: kube.ReadOnly(),
	}, listHandler(c))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "daemonset_pods",
		Description: "List the pods of one or more Kubernetes daemonsets in one call, with ready containers, status, restarts, node, age, and whether each pod is at the current or an old revision. Pass every daemonset of interest in daemonsets as {namespace, name}; a daemonset that does not exist has found false.",
		Annotations: kube.ReadOnly(),
	}, podsHandler(c))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "daemonset_describe",
		Description: "Describe up to 5 Kubernetes daemonsets in one call: health plus the full daemonset object as YAML (spec, pod template, update strategy, status), with literal environment values redacted. Pass daemonsets as {namespace, name}; a daemonset that does not exist has found false.",
		Annotations: kube.ReadOnly(),
	}, describeHandler(c))
}

func listHandler(c kube.Clients) mcp.ToolHandlerFor[listInput, DaemonSetListOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in listInput) (*mcp.CallToolResult, DaemonSetListOutput, error) {
		out, err := listDaemonSets(ctx, c.Core, in.Namespace)
		if err != nil {
			return nil, DaemonSetListOutput{}, err
		}
		return kube.TextResult(kube.FormatWorkloadList("daemonsets", out.Namespace, out.DaemonSets)), out, nil
	}
}

func podsHandler(c kube.Clients) mcp.ToolHandlerFor[targetInput, DaemonSetPodsOutput] {
	return kube.PerTarget(
		func(in targetInput) ([]kube.ObjectRef, error) {
			return kube.ObjectTargets("daemonsets", in.DaemonSets, kube.MaxTargets)
		},
		func(ctx context.Context, ref kube.ObjectRef) (kube.WorkloadPods, error) {
			return getPods(ctx, c.Core, ref)
		},
		kube.FormatWorkloadPods("daemonset"),
		func(d []kube.WorkloadPods) DaemonSetPodsOutput { return DaemonSetPodsOutput{DaemonSets: d} },
	)
}

func describeHandler(c kube.Clients) mcp.ToolHandlerFor[targetInput, DaemonSetDescribeOutput] {
	return kube.PerTarget(
		func(in targetInput) ([]kube.ObjectRef, error) {
			return kube.ObjectTargets("daemonsets", in.DaemonSets, kube.MaxDescribeTargets)
		},
		func(ctx context.Context, ref kube.ObjectRef) (kube.Described, error) {
			return describe(ctx, c.Core, ref)
		},
		kube.FormatDescribed("daemonset"),
		func(d []kube.Described) DaemonSetDescribeOutput { return DaemonSetDescribeOutput{DaemonSets: d} },
	)
}

// listDaemonSets reports counts against the nodes that should run a pod, so READY is ready over desired scheduled.
func listDaemonSets(ctx context.Context, client k8s.Interface, namespace string) (DaemonSetListOutput, error) {
	namespace = strings.TrimSpace(namespace)
	list, err := client.AppsV1().DaemonSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return DaemonSetListOutput{}, err
	}
	if err := kube.CheckNamespace(ctx, client, namespace, len(list.Items)); err != nil {
		return DaemonSetListOutput{}, err
	}
	out := DaemonSetListOutput{Namespace: namespace, DaemonSets: make([]kube.WorkloadListItem, 0, len(list.Items))}
	for i := range list.Items {
		d := &list.Items[i]
		out.DaemonSets = append(out.DaemonSets, kube.WorkloadListItem{
			Namespace: d.Namespace, Name: d.Name, Health: health(d), Desired: d.Status.DesiredNumberScheduled,
			Ready: d.Status.NumberReady, Updated: d.Status.UpdatedNumberScheduled, Available: d.Status.NumberAvailable,
			Created: d.CreationTimestamp.Time,
		})
	}
	return out, nil
}

// getDaemonSet returns nil and a note on a miss; the note names the namespace when that is what is missing.
func getDaemonSet(ctx context.Context, client k8s.Interface, ref kube.ObjectRef) (*appsv1.DaemonSet, string, error) {
	d, found, note, err := kube.GetNamespaced(ctx, client, ref, client.AppsV1().DaemonSets(ref.Namespace).Get)
	if !found {
		return nil, note, err
	}
	return d, "", nil
}

// health follows kubectl rollout status for daemonsets; no node matching the daemonset is NoNodes rather than a
// scale to zero, and an OnDelete update is never in progress, since it waits for pods to be deleted.
func health(d *appsv1.DaemonSet) string {
	desired := d.Status.DesiredNumberScheduled
	rolling := d.Spec.UpdateStrategy.Type != appsv1.OnDeleteDaemonSetStrategyType
	switch {
	case d.Status.ObservedGeneration < d.Generation:
		return healthProgressing
	case desired == 0:
		return healthNoNodes
	case rolling && d.Status.UpdatedNumberScheduled < desired:
		return healthProgressing
	case d.Status.NumberAvailable < desired:
		return healthDegraded
	default:
		return healthHealthy
	}
}
