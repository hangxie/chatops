package pod

import (
	"context"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"

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

// PodResourcesOutput is the structured result of pod_resources: one entry per named or selected pod.
type PodResourcesOutput struct {
	Namespaces []string       `json:"namespaces,omitempty"`
	Node       string         `json:"node,omitempty"`
	Pods       []PodResources `json:"pods"`
	// Total counts every pod a scope matched before the cap.
	Total int            `json:"total,omitempty"`
	Cut   []NamespaceCut `json:"cut,omitempty"`
}

type podResourcesInput struct {
	Pods       []kube.ObjectRef `json:"pods,omitempty" jsonschema:"the pods to report on, all in one call; omit to select by namespaces or node"`
	Namespaces []string         `json:"namespaces,omitempty" jsonschema:"report on every pod in these namespaces, all in one call, instead of naming pods"`
	Node       string           `json:"node,omitempty" jsonschema:"report on every pod on this node, instead of naming pods"`
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

func podResourcesHandler(c kube.Clients) mcp.ToolHandlerFor[podResourcesInput, PodResourcesOutput] {
	byName := kube.PerTarget(
		func(in podResourcesInput) ([]kube.ObjectRef, error) {
			return kube.ObjectTargets("pods", in.Pods, kube.MaxTargets)
		},
		func(ctx context.Context, ref kube.ObjectRef) (PodResources, error) {
			return getPodResources(ctx, c, ref)
		},
		formatPodResources,
		func(p []PodResources) PodResourcesOutput { return PodResourcesOutput{Pods: p} },
	)
	return func(ctx context.Context, req *mcp.CallToolRequest, in podResourcesInput) (*mcp.CallToolResult, PodResourcesOutput, error) {
		scoped := len(in.Namespaces) > 0 || strings.TrimSpace(in.Node) != ""
		switch {
		case len(in.Pods) > 0 && scoped:
			return nil, PodResourcesOutput{}, errors.New("pass either pods or namespaces and node, not both")
		case len(in.Pods) > 0:
			return byName(ctx, req, in)
		case !scoped:
			return nil, PodResourcesOutput{}, errors.New("pass pods, or namespaces or node to select them")
		}
		out, err := scopedPodResources(ctx, c, in.Namespaces, in.Node, c.ResultLimit())
		if err != nil {
			return nil, PodResourcesOutput{}, err
		}
		return kube.TextResult(formatScopedResources(out)), out, nil
	}
}

// scopedPodResources makes one pod list and one metrics list, where naming each pod costs two calls per pod.
func scopedPodResources(ctx context.Context, c kube.Clients, namespaces []string, node string, limit int) (PodResourcesOutput, error) {
	sel, err := selectPods(ctx, c.Core, namespaces, node, limit)
	if err != nil {
		return PodResourcesOutput{}, err
	}
	out := PodResourcesOutput{Namespaces: sel.namespaces, Node: sel.node, Pods: make([]PodResources, 0, len(sel.pods)), Total: sel.total, Cut: sel.cut}
	if len(sel.pods) == 0 {
		return out, nil
	}
	usage := map[kube.ObjectRef]map[string]Amounts{}
	usageNote := ""
	metrics, err := listPodMetrics(ctx, c, sel.pods)
	if err != nil {
		usageNote = "cpu and memory usage unavailable: " + err.Error()
	}
	for _, m := range metrics {
		usage[kube.ObjectRef{Namespace: m.Namespace, Name: m.Name}] = containerUsage(m.Containers)
	}
	for _, p := range sel.pods {
		pu, ok := usage[kube.ObjectRef{Namespace: p.Namespace, Name: p.Name}]
		note := usageNote
		if !ok && note == "" {
			note = "no usage reported for this pod"
		}
		out.Pods = append(out.Pods, podResources(p, pu, note))
	}
	return out, nil
}

// listPodMetrics falls back to a list per namespace when the cluster-wide list is forbidden.
func listPodMetrics(ctx context.Context, c kube.Clients, pods []*corev1.Pod) ([]metricsv1beta1.PodMetrics, error) {
	var namespaces []string
	seen := map[string]bool{}
	for _, p := range pods {
		if !seen[p.Namespace] {
			seen[p.Namespace] = true
			namespaces = append(namespaces, p.Namespace)
		}
	}
	list := func(ctx context.Context, namespace string) ([]metricsv1beta1.PodMetrics, error) {
		l, err := c.Metrics.MetricsV1beta1().PodMetricses(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		return l.Items, nil
	}
	if len(namespaces) == 1 {
		return list(ctx, namespaces[0])
	}
	all, err := list(ctx, "")
	if !apierrors.IsForbidden(err) {
		return all, err
	}
	lists, err := kube.FetchAll(ctx, namespaces, list)
	if err != nil {
		return nil, err
	}
	for _, l := range lists {
		all = append(all, l...)
	}
	return all, nil
}

// getPodResources degrades to a usage note when the metrics API cannot answer, as node_resources does.
func getPodResources(ctx context.Context, c kube.Clients, ref kube.ObjectRef) (PodResources, error) {
	out := PodResources{Namespace: ref.Namespace, Name: ref.Name}
	p, note, err := getPod(ctx, c.Core, ref)
	if err != nil || p == nil {
		out.Note = note
		return out, err
	}
	metrics, err := c.Metrics.MetricsV1beta1().PodMetricses(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return podResources(p, nil, "cpu and memory usage unavailable: "+err.Error()), nil
	}
	return podResources(p, containerUsage(metrics.Containers), ""), nil
}

func containerUsage(containers []metricsv1beta1.ContainerMetrics) map[string]Amounts {
	usage := make(map[string]Amounts, len(containers))
	for _, cm := range containers {
		usage[cm.Name] = amounts(cm.Usage)
	}
	return usage
}

// podResources reports p's running containers against usage, which is keyed by container name.
func podResources(p *corev1.Pod, usage map[string]Amounts, usageNote string) PodResources {
	out := PodResources{Namespace: p.Namespace, Name: p.Name, Found: true, UsageNote: usageNote}
	for _, ctr := range runningContainers(p) {
		cr := ContainerResources{Container: ctr.Name, Requests: amounts(ctr.Resources.Requests), Limits: amounts(ctr.Resources.Limits)}
		if u, ok := usage[ctr.Name]; ok {
			cr.Usage = &u
		}
		out.Containers = append(out.Containers, cr)
	}
	return out
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
