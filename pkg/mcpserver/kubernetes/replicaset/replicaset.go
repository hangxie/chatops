// Package replicaset serves the read-only replicaset tools: health, owned pods, and cleaned definition.
package replicaset

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8s "k8s.io/client-go/kubernetes"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

// Health words; see docs/mcp-k8s.md for the rules.
const (
	healthScaledToZero = "ScaledToZero"
	healthProgressing  = "Progressing"
	healthDegraded     = "Degraded"
	healthHealthy      = "Healthy"
)

// ReplicaSetListItem is one replicaset's roster entry; Owner is its controller as Kind/name, usually a deployment.
type ReplicaSetListItem struct {
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	Health    string    `json:"health"`
	Desired   int32     `json:"desired"`
	Current   int32     `json:"current"`
	Ready     int32     `json:"ready"`
	Owner     string    `json:"owner,omitempty"`
	Created   time.Time `json:"created"`
}

// ReplicaSetListOutput is the structured result of replicaset_list; Namespace is empty when listing all.
type ReplicaSetListOutput struct {
	Namespace   string               `json:"namespace,omitempty"`
	ReplicaSets []ReplicaSetListItem `json:"replicasets"`
}

type listInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"the namespace to list; omit to list every namespace"`
}

type targetInput struct {
	ReplicaSets []kube.ObjectRef `json:"replicasets" jsonschema:"the replicasets to report on, all in one call"`
}

// Register adds the replicaset tools to server.
func Register(server *mcp.Server, c kube.Clients) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "replicaset_list",
		Description: "List Kubernetes replicasets with their health (Healthy, Progressing, Degraded, ScaledToZero), desired, current, and ready replicas, and owning deployment. Pass namespace to list one namespace; omit it to list every namespace.",
		Annotations: kube.ReadOnly(),
	}, listHandler(c))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "replicaset_pods",
		Description: "List the pods of one or more Kubernetes replicasets in one call, with ready containers, status, restarts, node, and age. Pass every replicaset of interest in replicasets as {namespace, name}; a replicaset that does not exist has found false.",
		Annotations: kube.ReadOnly(),
	}, podsHandler(c))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "replicaset_describe",
		Description: "Describe up to 5 Kubernetes replicasets in one call: health plus the full replicaset object as YAML (spec, pod template, status), with literal environment values redacted. Pass replicasets as {namespace, name}; a replicaset that does not exist has found false.",
		Annotations: kube.ReadOnly(),
	}, describeHandler(c))
}

func listHandler(c kube.Clients) mcp.ToolHandlerFor[listInput, ReplicaSetListOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in listInput) (*mcp.CallToolResult, ReplicaSetListOutput, error) {
		out, err := listReplicaSets(ctx, c.Core, in.Namespace)
		if err != nil {
			return nil, ReplicaSetListOutput{}, err
		}
		return kube.TextResult(formatList(out)), out, nil
	}
}

func podsHandler(c kube.Clients) mcp.ToolHandlerFor[targetInput, ReplicaSetPodsOutput] {
	return kube.PerTarget(
		func(in targetInput) ([]kube.ObjectRef, error) {
			return kube.ObjectTargets("replicasets", in.ReplicaSets, kube.MaxTargets)
		},
		func(ctx context.Context, ref kube.ObjectRef) (kube.WorkloadPods, error) {
			return getPods(ctx, c.Core, ref)
		},
		kube.FormatWorkloadPods("replicaset"),
		func(r []kube.WorkloadPods) ReplicaSetPodsOutput { return ReplicaSetPodsOutput{ReplicaSets: r} },
	)
}

func describeHandler(c kube.Clients) mcp.ToolHandlerFor[targetInput, ReplicaSetDescribeOutput] {
	return kube.PerTarget(
		func(in targetInput) ([]kube.ObjectRef, error) {
			return kube.ObjectTargets("replicasets", in.ReplicaSets, kube.MaxDescribeTargets)
		},
		func(ctx context.Context, ref kube.ObjectRef) (kube.Described, error) {
			return describe(ctx, c.Core, ref)
		},
		kube.FormatDescribed("replicaset"),
		func(r []kube.Described) ReplicaSetDescribeOutput { return ReplicaSetDescribeOutput{ReplicaSets: r} },
	)
}

func listReplicaSets(ctx context.Context, client k8s.Interface, namespace string) (ReplicaSetListOutput, error) {
	namespace = strings.TrimSpace(namespace)
	list, err := client.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return ReplicaSetListOutput{}, err
	}
	if err := kube.CheckNamespace(ctx, client, namespace, len(list.Items)); err != nil {
		return ReplicaSetListOutput{}, err
	}
	out := ReplicaSetListOutput{Namespace: namespace, ReplicaSets: make([]ReplicaSetListItem, 0, len(list.Items))}
	for i := range list.Items {
		r := &list.Items[i]
		item := ReplicaSetListItem{
			Namespace: r.Namespace, Name: r.Name, Health: health(r), Desired: desiredReplicas(r),
			Current: r.Status.Replicas, Ready: r.Status.ReadyReplicas, Created: r.CreationTimestamp.Time,
		}
		if ref := metav1.GetControllerOf(r); ref != nil {
			item.Owner = ref.Kind + "/" + ref.Name
		}
		out.ReplicaSets = append(out.ReplicaSets, item)
	}
	return out, nil
}

func formatList(out ReplicaSetListOutput) string {
	if len(out.ReplicaSets) == 0 {
		return kube.FormatWorkloadList("replicasets", out.Namespace, nil)
	}
	rows := make([][]string, 0, len(out.ReplicaSets))
	for _, r := range out.ReplicaSets {
		rows = append(rows, []string{
			r.Namespace, r.Name, r.Health, strconv.Itoa(int(r.Desired)), strconv.Itoa(int(r.Current)),
			fmt.Sprintf("%d/%d", r.Ready, r.Desired), kube.OrNone(r.Owner), kube.Age(r.Created),
		})
	}
	return kube.Table([]string{"NAMESPACE", "NAME", "HEALTH", "DESIRED", "CURRENT", "READY", "OWNER", "AGE"}, rows)
}

// getReplicaSet returns nil and a note on a miss; the note names the namespace when that is what is missing.
func getReplicaSet(ctx context.Context, client k8s.Interface, ref kube.ObjectRef) (*appsv1.ReplicaSet, string, error) {
	r, found, note, err := kube.GetNamespaced(ctx, client, ref, client.AppsV1().ReplicaSets(ref.Namespace).Get)
	if !found {
		return nil, note, err
	}
	return r, "", nil
}

// health has no rollout to follow: a replicaset is Progressing while it scales, Degraded when short of available.
func health(r *appsv1.ReplicaSet) string {
	desired := desiredReplicas(r)
	switch {
	case desired == 0 && r.Status.Replicas == 0:
		return healthScaledToZero
	case r.Status.ObservedGeneration < r.Generation, r.Status.Replicas != desired:
		return healthProgressing
	case r.Status.AvailableReplicas < desired:
		return healthDegraded
	default:
		return healthHealthy
	}
}

// desiredReplicas applies the API default of 1 when spec.replicas is unset.
func desiredReplicas(r *appsv1.ReplicaSet) int32 {
	if r.Spec.Replicas == nil {
		return 1
	}
	return *r.Spec.Replicas
}
