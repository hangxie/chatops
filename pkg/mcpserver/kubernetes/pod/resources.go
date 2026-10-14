package pod

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

// Amounts holds CPU and memory quantities in canonical form; an unset one is empty.
type Amounts struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
}

// ContainerResources is one container's requests, limits, and usage; Usage is nil when metrics are unavailable.
type ContainerResources struct {
	Container string   `json:"container"`
	Requests  Amounts  `json:"requests"`
	Limits    Amounts  `json:"limits"`
	Usage     *Amounts `json:"usage,omitempty"`
}

// PodResourcesOutput is the structured result of pod_resources: one entry per requested pod, in request order.
type PodResourcesOutput struct {
	Pods []PodResources `json:"pods"`
}

// PodResources is one pod's per-container resources; UsageNote explains missing usage.
type PodResources struct {
	Namespace  string               `json:"namespace"`
	Name       string               `json:"name"`
	Found      bool                 `json:"found"`
	Note       string               `json:"note,omitempty"`
	UsageNote  string               `json:"usage_note,omitempty"`
	Containers []ContainerResources `json:"containers,omitempty"`
}

func podResourcesHandler(c kube.Clients) mcp.ToolHandlerFor[podInput, PodResourcesOutput] {
	return kube.PerTarget(
		func(in podInput) ([]kube.ObjectRef, error) {
			return kube.ObjectTargets("pods", in.Pods, kube.MaxTargets)
		},
		func(ctx context.Context, ref kube.ObjectRef) (PodResources, error) {
			return getPodResources(ctx, c, ref)
		},
		formatPodResources,
		func(p []PodResources) PodResourcesOutput { return PodResourcesOutput{Pods: p} },
	)
}

// getPodResources degrades to a usage note when the metrics API cannot answer, as node_resources does.
func getPodResources(ctx context.Context, c kube.Clients, ref kube.ObjectRef) (PodResources, error) {
	out := PodResources{Namespace: ref.Namespace, Name: ref.Name}
	p, note, err := getPod(ctx, c.Core, ref)
	if err != nil || p == nil {
		out.Note = note
		return out, err
	}
	out.Found = true
	usage := map[string]Amounts{}
	metrics, err := c.Metrics.MetricsV1beta1().PodMetricses(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		out.UsageNote = "cpu and memory usage unavailable: " + err.Error()
	} else {
		for _, cm := range metrics.Containers {
			usage[cm.Name] = amounts(cm.Usage)
		}
	}
	for _, ctr := range runningContainers(p) {
		cr := ContainerResources{Container: ctr.Name, Requests: amounts(ctr.Resources.Requests), Limits: amounts(ctr.Resources.Limits)}
		if u, ok := usage[ctr.Name]; ok {
			cr.Usage = &u
		}
		out.Containers = append(out.Containers, cr)
	}
	return out, nil
}

// runningContainers lists native sidecars before the main containers, since both consume resources for the pod's life.
func runningContainers(p *corev1.Pod) []corev1.Container {
	var out []corev1.Container
	for _, c := range p.Spec.InitContainers {
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			out = append(out, c)
		}
	}
	return append(out, p.Spec.Containers...)
}

func amounts(rl corev1.ResourceList) Amounts {
	var a Amounts
	if q, ok := rl[corev1.ResourceCPU]; ok {
		a.CPU = q.String()
	}
	if q, ok := rl[corev1.ResourceMemory]; ok {
		a.Memory = q.String()
	}
	return a
}
