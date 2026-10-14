// Package deployment serves the read-only deployment tools: health, owned pods, and cleaned definition.
package deployment

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8s "k8s.io/client-go/kubernetes"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

// Health words shared by every workload tool; see docs/mcp-k8s.md for the rules.
const (
	healthPaused       = "Paused"
	healthStalled      = "Stalled"
	healthScaledToZero = "ScaledToZero"
	healthProgressing  = "Progressing"
	healthDegraded     = "Degraded"
	healthHealthy      = "Healthy"
)

// DeploymentListOutput is the structured result of deployment_list; Namespace is empty when listing all.
type DeploymentListOutput struct {
	Namespace   string                  `json:"namespace,omitempty"`
	Deployments []kube.WorkloadListItem `json:"deployments"`
	// Total counts every match before the cap.
	Total int `json:"total"`
}

type deploymentListInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"the namespace to list; omit to list every namespace"`
}

type deploymentInput struct {
	Deployments []kube.ObjectRef `json:"deployments" jsonschema:"the deployments to report on, all in one call"`
}

// Register adds the deployment tools to server.
func Register(server *mcp.Server, c kube.Clients) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "deployment_list",
		Description: "List Kubernetes deployments with their health (Healthy, Progressing, Degraded, Stalled, Paused, ScaledToZero) and ready, up-to-date, and available replicas. Pass namespace to list one namespace; omit it to list every namespace.",
		Annotations: kube.ReadOnly(),
	}, deploymentListHandler(c))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "deployment_pods",
		Description: "List the pods of one or more Kubernetes deployments in one call, with ready containers, status, restarts, node, age, and whether each pod is from the current or an old rollout revision. Pass every deployment of interest in deployments as {namespace, name}; a deployment that does not exist has found false.",
		Annotations: kube.ReadOnly(),
	}, deploymentPodsHandler(c))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "deployment_describe",
		Description: "Describe up to 5 Kubernetes deployments in one call: health plus the full deployment object as YAML (spec, pod template, conditions, replica status), with literal environment values redacted. Pass deployments as {namespace, name}; a deployment that does not exist has found false.",
		Annotations: kube.ReadOnly(),
	}, deploymentDescribeHandler(c))
}

func deploymentListHandler(c kube.Clients) mcp.ToolHandlerFor[deploymentListInput, DeploymentListOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in deploymentListInput) (*mcp.CallToolResult, DeploymentListOutput, error) {
		out, err := listDeployments(ctx, c.Core, in.Namespace)
		if err != nil {
			return nil, DeploymentListOutput{}, err
		}
		out.Deployments, out.Total = kube.Cap(out.Deployments, c.ResultLimit())
		return kube.TextResult(kube.FormatWorkloadList("deployments", out.Namespace, out.Deployments, out.Total)), out, nil
	}
}

func deploymentPodsHandler(c kube.Clients) mcp.ToolHandlerFor[deploymentInput, DeploymentPodsOutput] {
	return kube.PerTarget(
		func(in deploymentInput) ([]kube.ObjectRef, error) {
			return kube.ObjectTargets("deployments", in.Deployments, kube.MaxTargets)
		},
		func(ctx context.Context, ref kube.ObjectRef) (kube.WorkloadPods, error) {
			w, err := getDeploymentPods(ctx, c.Core, ref)
			return w.Capped(c.ResultLimit()), err
		},
		kube.FormatWorkloadPods("deployment"),
		func(d []kube.WorkloadPods) DeploymentPodsOutput { return DeploymentPodsOutput{Deployments: d} },
	)
}

func deploymentDescribeHandler(c kube.Clients) mcp.ToolHandlerFor[deploymentInput, DeploymentDescribeOutput] {
	return kube.PerTarget(
		func(in deploymentInput) ([]kube.ObjectRef, error) {
			return kube.ObjectTargets("deployments", in.Deployments, kube.MaxDescribeTargets)
		},
		func(ctx context.Context, ref kube.ObjectRef) (kube.Described, error) {
			return describeDeployment(ctx, c.Core, ref)
		},
		kube.FormatDescribed("deployment"),
		func(d []kube.Described) DeploymentDescribeOutput { return DeploymentDescribeOutput{Deployments: d} },
	)
}

func listDeployments(ctx context.Context, client k8s.Interface, namespace string) (DeploymentListOutput, error) {
	namespace = strings.TrimSpace(namespace)
	list, err := client.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return DeploymentListOutput{}, err
	}
	if err := kube.CheckNamespace(ctx, client, namespace, len(list.Items)); err != nil {
		return DeploymentListOutput{}, err
	}
	out := DeploymentListOutput{Namespace: namespace, Deployments: make([]kube.WorkloadListItem, 0, len(list.Items))}
	for i := range list.Items {
		d := &list.Items[i]
		out.Deployments = append(out.Deployments, kube.WorkloadListItem{
			Namespace: d.Namespace,
			Name:      d.Name,
			Health:    deploymentHealth(d),
			Desired:   desiredReplicas(d),
			Ready:     d.Status.ReadyReplicas,
			Updated:   d.Status.UpdatedReplicas,
			Available: d.Status.AvailableReplicas,
			Created:   d.CreationTimestamp.Time,
		})
	}
	return out, nil
}

// getDeployment returns nil and a note on a miss; the note names the namespace when that is what is missing.
func getDeployment(ctx context.Context, client k8s.Interface, ref kube.ObjectRef) (*appsv1.Deployment, string, error) {
	d, found, note, err := kube.GetNamespaced(ctx, client, ref, client.AppsV1().Deployments(ref.Namespace).Get)
	if !found {
		return nil, note, err
	}
	return d, "", nil
}

// deploymentHealth checks in order so the most actionable state wins; unlike kubectl rollout status, surplus
// replicas during a scale-down are still Progressing.
func deploymentHealth(d *appsv1.Deployment) string {
	desired := desiredReplicas(d)
	switch {
	case d.Spec.Paused:
		return healthPaused
	case progressDeadlineExceeded(d):
		return healthStalled
	case desired == 0:
		return healthScaledToZero
	case d.Status.ObservedGeneration < d.Generation,
		d.Status.UpdatedReplicas < desired,
		d.Status.Replicas > d.Status.UpdatedReplicas,
		d.Status.Replicas > desired:
		return healthProgressing
	case d.Status.AvailableReplicas < desired:
		return healthDegraded
	default:
		return healthHealthy
	}
}

func progressDeadlineExceeded(d *appsv1.Deployment) bool {
	for _, c := range d.Status.Conditions {
		if c.Type == appsv1.DeploymentProgressing && c.Status == corev1.ConditionFalse && c.Reason == "ProgressDeadlineExceeded" {
			return true
		}
	}
	return false
}

// desiredReplicas applies the API default of 1 when spec.replicas is unset.
func desiredReplicas(d *appsv1.Deployment) int32 {
	if d.Spec.Replicas == nil {
		return 1
	}
	return *d.Spec.Replicas
}
