package node

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8s "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// ResourceAmounts holds one node's amounts for the four tracked resources; disk is ephemeral-storage.
type ResourceAmounts struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
	Disk   string `json:"disk,omitempty"`
	Pods   string `json:"pods,omitempty"`
}

// NodeResourcesOutput is the structured result of node_resources: one entry per requested node, in request order.
type NodeResourcesOutput struct {
	Nodes []NodeResources `json:"nodes"`
}

// NodeResources is one node's capacity and utilization; detail fields are omitted on a miss.
type NodeResources struct {
	Node        string           `json:"node"`
	Found       bool             `json:"found"`
	Capacity    *ResourceAmounts `json:"capacity,omitempty"`
	Allocatable *ResourceAmounts `json:"allocatable,omitempty"`
	// Usage is current utilization: cpu and memory from the metrics API, pods from a live count, disk (when
	// RBAC allows the kubelet Summary API) the kubelet filesystem used bytes.
	Usage *ResourceAmounts `json:"usage,omitempty"`
	// DiskTotal is the kubelet filesystem capacity in bytes (Summary API), the denominator for Usage.Disk.
	// It describes the node root filesystem and is distinct from the ephemeral-storage capacity/allocatable above.
	DiskTotal string `json:"disk_total,omitempty"`
	UsageNote string `json:"usage_note,omitempty"`
}

func getNodeResources(ctx context.Context, c clients, name string) (NodeResources, error) {
	n, err := c.core.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return NodeResources{Node: name, Found: false}, nil
	}
	if err != nil {
		return NodeResources{}, err
	}

	out := NodeResources{
		Node:        name,
		Found:       true,
		Capacity:    amounts(n.Status.Capacity),
		Allocatable: amounts(n.Status.Allocatable),
		Usage:       &ResourceAmounts{},
	}
	var notes []string

	// CPU and memory utilization come from the metrics API.
	if nm, err := c.metrics.MetricsV1beta1().NodeMetricses().Get(ctx, name, metav1.GetOptions{}); err != nil {
		notes = append(notes, "cpu and memory utilization unavailable: "+err.Error())
	} else {
		out.Usage.CPU = nm.Usage.Cpu().String()
		out.Usage.Memory = nm.Usage.Memory().String()
	}

	// Pod count comes from the pod API; a failure degrades to a note rather than losing the rest of the report.
	if pods, err := countPods(ctx, c.core, name); err != nil {
		notes = append(notes, "pod count unavailable: "+err.Error())
	} else {
		out.Usage.Pods = strconv.Itoa(pods)
	}

	// Disk utilization comes from the kubelet Summary API, gated only by the nodes/proxy RBAC grant.
	// Its denominator is the kubelet filesystem capacity, not the ephemeral-storage allocatable above.
	if used, total, ok, err := c.diskUsage(ctx, name); apierrors.IsForbidden(err) {
		notes = append(notes, "disk utilization not permitted: grant get on nodes/proxy to read the kubelet Summary API")
	} else if err != nil {
		notes = append(notes, "disk utilization unavailable: "+err.Error())
	} else if !ok {
		notes = append(notes, "disk utilization unavailable: kubelet Summary API reported no node filesystem usage")
	} else {
		out.Usage.Disk = strconv.FormatInt(used, 10)
		if total > 0 {
			out.DiskTotal = strconv.FormatInt(total, 10)
		}
	}

	out.UsageNote = strings.Join(notes, "; ")
	return out, nil
}

// kubeletSummary is the slice of the kubelet /stats/summary response node_resources reads: node filesystem
// used and total bytes, the matching pair for filesystem fullness.
type kubeletSummary struct {
	Node struct {
		Fs struct {
			UsedBytes     *int64 `json:"usedBytes"`
			CapacityBytes *int64 `json:"capacityBytes"`
		} `json:"fs"`
	} `json:"node"`
}

// summaryDiskUsage reads node filesystem used and total bytes from the kubelet Summary API, proxied through the
// API server. It reports Kubernetes-managed filesystem usage only, not every host-mounted disk.
func summaryDiskUsage(rc rest.Interface) func(context.Context, string) (used, total int64, ok bool, err error) {
	return func(ctx context.Context, node string) (int64, int64, bool, error) {
		raw, err := rc.Get().Resource("nodes").Name(node).SubResource("proxy").Suffix("stats", "summary").DoRaw(ctx)
		if err != nil {
			return 0, 0, false, err
		}
		var s kubeletSummary
		if err := json.Unmarshal(raw, &s); err != nil {
			return 0, 0, false, err
		}
		if s.Node.Fs.UsedBytes == nil {
			return 0, 0, false, nil
		}
		var total int64
		if s.Node.Fs.CapacityBytes != nil {
			total = *s.Node.Fs.CapacityBytes
		}
		return *s.Node.Fs.UsedBytes, total, true, nil
	}
}

// amounts pulls the four tracked resources from a ResourceList, keeping canonical quantity strings.
func amounts(rl corev1.ResourceList) *ResourceAmounts {
	a := &ResourceAmounts{}
	if q, ok := rl[corev1.ResourceCPU]; ok {
		a.CPU = q.String()
	}
	if q, ok := rl[corev1.ResourceMemory]; ok {
		a.Memory = q.String()
	}
	if q, ok := rl[corev1.ResourceEphemeralStorage]; ok {
		a.Disk = q.String()
	}
	if q, ok := rl[corev1.ResourcePods]; ok {
		a.Pods = q.String()
	}
	return a
}

// countPods counts the non-terminated pods scheduled on the node; the field selector narrows the real API call,
// and the explicit nodeName check keeps the count correct against clients that do not apply field selectors.
func countPods(ctx context.Context, core k8s.Interface, node string) (int, error) {
	list, err := core.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("spec.nodeName=%s", node),
	})
	if err != nil {
		return 0, err
	}
	count := 0
	for i := range list.Items {
		p := &list.Items[i]
		if p.Spec.NodeName != node {
			continue
		}
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		count++
	}
	return count, nil
}
