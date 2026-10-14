// Package statefulset serves the read-only statefulset tools: health, owned pods, and cleaned definition.
package statefulset

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
	healthScaledToZero = "ScaledToZero"
	healthProgressing  = "Progressing"
	healthDegraded     = "Degraded"
	healthHealthy      = "Healthy"
)

// StatefulSetListOutput is the structured result of statefulset_list; Namespace is empty when listing all.
type StatefulSetListOutput struct {
	Namespace    string                  `json:"namespace,omitempty"`
	StatefulSets []kube.WorkloadListItem `json:"statefulsets"`
}

type listInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"the namespace to list; omit to list every namespace"`
}

type targetInput struct {
	StatefulSets []kube.ObjectRef `json:"statefulsets" jsonschema:"the statefulsets to report on, all in one call"`
}

// Register adds the statefulset tools to server.
func Register(server *mcp.Server, c kube.Clients) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "statefulset_list",
		Description: "List Kubernetes statefulsets with their health (Healthy, Progressing, Degraded, ScaledToZero) and ready, up-to-date, and available replicas. Pass namespace to list one namespace; omit it to list every namespace.",
		Annotations: kube.ReadOnly(),
	}, listHandler(c))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "statefulset_pods",
		Description: "List the pods of one or more Kubernetes statefulsets in one call, with ready containers, status, restarts, node, age, and whether each pod is at the current or an old revision. Pass every statefulset of interest in statefulsets as {namespace, name}; a statefulset that does not exist has found false.",
		Annotations: kube.ReadOnly(),
	}, podsHandler(c))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "statefulset_describe",
		Description: "Describe up to 5 Kubernetes statefulsets in one call: health plus the full statefulset object as YAML (spec, pod and volume claim templates, update strategy, status), with literal environment values redacted. Pass statefulsets as {namespace, name}; a statefulset that does not exist has found false.",
		Annotations: kube.ReadOnly(),
	}, describeHandler(c))
}

func listHandler(c kube.Clients) mcp.ToolHandlerFor[listInput, StatefulSetListOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in listInput) (*mcp.CallToolResult, StatefulSetListOutput, error) {
		out, err := listStatefulSets(ctx, c.Core, in.Namespace)
		if err != nil {
			return nil, StatefulSetListOutput{}, err
		}
		return kube.TextResult(kube.FormatWorkloadList("statefulsets", out.Namespace, out.StatefulSets)), out, nil
	}
}

func podsHandler(c kube.Clients) mcp.ToolHandlerFor[targetInput, StatefulSetPodsOutput] {
	return kube.PerTarget(
		func(in targetInput) ([]kube.ObjectRef, error) {
			return kube.ObjectTargets("statefulsets", in.StatefulSets, kube.MaxTargets)
		},
		func(ctx context.Context, ref kube.ObjectRef) (kube.WorkloadPods, error) {
			return getPods(ctx, c.Core, ref)
		},
		kube.FormatWorkloadPods("statefulset"),
		func(s []kube.WorkloadPods) StatefulSetPodsOutput { return StatefulSetPodsOutput{StatefulSets: s} },
	)
}

func describeHandler(c kube.Clients) mcp.ToolHandlerFor[targetInput, StatefulSetDescribeOutput] {
	return kube.PerTarget(
		func(in targetInput) ([]kube.ObjectRef, error) {
			return kube.ObjectTargets("statefulsets", in.StatefulSets, kube.MaxDescribeTargets)
		},
		func(ctx context.Context, ref kube.ObjectRef) (kube.Described, error) {
			return describe(ctx, c.Core, ref)
		},
		kube.FormatDescribed("statefulset"),
		func(s []kube.Described) StatefulSetDescribeOutput { return StatefulSetDescribeOutput{StatefulSets: s} },
	)
}

func listStatefulSets(ctx context.Context, client k8s.Interface, namespace string) (StatefulSetListOutput, error) {
	namespace = strings.TrimSpace(namespace)
	list, err := client.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return StatefulSetListOutput{}, err
	}
	if err := kube.CheckNamespace(ctx, client, namespace, len(list.Items)); err != nil {
		return StatefulSetListOutput{}, err
	}
	out := StatefulSetListOutput{Namespace: namespace, StatefulSets: make([]kube.WorkloadListItem, 0, len(list.Items))}
	for i := range list.Items {
		s := &list.Items[i]
		out.StatefulSets = append(out.StatefulSets, kube.WorkloadListItem{
			Namespace: s.Namespace, Name: s.Name, Health: health(s), Desired: desiredReplicas(s),
			Ready: s.Status.ReadyReplicas, Updated: s.Status.UpdatedReplicas, Available: s.Status.AvailableReplicas,
			Created: s.CreationTimestamp.Time,
		})
	}
	return out, nil
}

// getStatefulSet returns nil and a note on a miss; the note names the namespace when that is what is missing.
func getStatefulSet(ctx context.Context, client k8s.Interface, ref kube.ObjectRef) (*appsv1.StatefulSet, string, error) {
	s, found, note, err := kube.GetNamespaced(ctx, client, ref, client.AppsV1().StatefulSets(ref.Namespace).Get)
	if !found {
		return nil, note, err
	}
	return s, "", nil
}

// health follows kubectl rollout status for statefulsets, partitions included; surplus replicas during a
// scale-down are still Progressing, as for deployments.
func health(s *appsv1.StatefulSet) string {
	desired := desiredReplicas(s)
	switch {
	case desired == 0:
		return healthScaledToZero
	case s.Status.ObservedGeneration < s.Generation, rolloutPending(s, desired), s.Status.Replicas > desired:
		return healthProgressing
	case s.Status.AvailableReplicas < desired:
		return healthDegraded
	default:
		return healthHealthy
	}
}

// rolloutPending reports an unfinished rolling update; a partitioned one is done once every pod at or above the
// partition is updated, and OnDelete updates only happen when pods are deleted, so they are never pending.
func rolloutPending(s *appsv1.StatefulSet, desired int32) bool {
	strategy := s.Spec.UpdateStrategy
	if strategy.Type == appsv1.OnDeleteStatefulSetStrategyType {
		return false
	}
	if ru := strategy.RollingUpdate; ru != nil && ru.Partition != nil && *ru.Partition > 0 {
		return s.Status.UpdatedReplicas < desired-*ru.Partition
	}
	return s.Status.UpdatedReplicas < desired || s.Status.CurrentRevision != s.Status.UpdateRevision
}

// desiredReplicas applies the API default of 1 when spec.replicas is unset.
func desiredReplicas(s *appsv1.StatefulSet) int32 {
	if s.Spec.Replicas == nil {
		return 1
	}
	return *s.Spec.Replicas
}
