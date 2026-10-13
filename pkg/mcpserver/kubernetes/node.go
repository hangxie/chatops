// Package kubernetes serves read-only Kubernetes inspection tools over MCP.
package kubernetes

import (
	"context"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8s "k8s.io/client-go/kubernetes"
)

const rolePrefix = "node-role.kubernetes.io/"

// NodeListItem is one node's roster entry.
type NodeListItem struct {
	Name    string    `json:"name"`
	Ready   string    `json:"ready"`
	Roles   []string  `json:"roles"`
	Created time.Time `json:"created"`
	Version string    `json:"version"`
}

// NodeListOutput is the structured result of node_list.
type NodeListOutput struct {
	Nodes []NodeListItem `json:"nodes"`
}

// NodeStatusOutput is the structured result of node_status: one entry per requested node, in request order.
type NodeStatusOutput struct {
	Nodes []NodeStatus `json:"nodes"`
}

// NodeStatus is one node's health; detail fields are omitted on a miss.
type NodeStatus struct {
	Node        string            `json:"node"`
	Found       bool              `json:"found"`
	Ready       string            `json:"ready,omitempty"`
	Roles       []string          `json:"roles,omitempty"`
	Version     string            `json:"version,omitempty"`
	Conditions  map[string]string `json:"conditions,omitempty"`
	Schedulable *bool             `json:"schedulable,omitempty"`
	Taints      []string          `json:"taints,omitempty"`
	Created     *time.Time        `json:"created,omitempty"`
}

func listNodes(ctx context.Context, client k8s.Interface) (NodeListOutput, error) {
	list, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return NodeListOutput{}, err
	}
	out := NodeListOutput{Nodes: make([]NodeListItem, 0, len(list.Items))}
	for i := range list.Items {
		n := &list.Items[i]
		out.Nodes = append(out.Nodes, NodeListItem{
			Name:    n.Name,
			Ready:   readyStatus(n),
			Roles:   roles(n),
			Created: n.CreationTimestamp.Time,
			Version: n.Status.NodeInfo.KubeletVersion,
		})
	}
	return out, nil
}

func getNodeStatus(ctx context.Context, client k8s.Interface, name string) (NodeStatus, error) {
	n, err := client.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return NodeStatus{Node: name, Found: false}, nil
	}
	if err != nil {
		return NodeStatus{}, err
	}
	schedulable := !n.Spec.Unschedulable
	created := n.CreationTimestamp.Time
	return NodeStatus{
		Node:        name,
		Found:       true,
		Ready:       readyStatus(n),
		Roles:       roles(n),
		Version:     n.Status.NodeInfo.KubeletVersion,
		Conditions:  conditions(n),
		Schedulable: &schedulable,
		Taints:      taints(n),
		Created:     &created,
	}, nil
}

// readyStatus derives the roster status from the Ready condition, preserving Unknown and absent as Unknown.
func readyStatus(n *corev1.Node) string {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			switch c.Status {
			case corev1.ConditionTrue:
				return "Ready"
			case corev1.ConditionFalse:
				return "NotReady"
			default:
				return "Unknown"
			}
		}
	}
	return "Unknown"
}

func roles(n *corev1.Node) []string {
	var r []string
	for label := range n.Labels {
		if role := strings.TrimPrefix(label, rolePrefix); role != label && role != "" {
			r = append(r, role)
		}
	}
	sort.Strings(r)
	return r
}

// conditions preserves each reported condition's raw status; absent conditions stay absent, never manufactured.
func conditions(n *corev1.Node) map[string]string {
	if len(n.Status.Conditions) == 0 {
		return nil
	}
	m := make(map[string]string, len(n.Status.Conditions))
	for _, c := range n.Status.Conditions {
		m[string(c.Type)] = string(c.Status)
	}
	return m
}

func taints(n *corev1.Node) []string {
	if len(n.Spec.Taints) == 0 {
		return nil
	}
	t := make([]string, 0, len(n.Spec.Taints))
	for _, taint := range n.Spec.Taints {
		s := taint.Key
		if taint.Value != "" {
			s += "=" + taint.Value
		}
		t = append(t, s+":"+string(taint.Effect))
	}
	sort.Strings(t)
	return t
}
