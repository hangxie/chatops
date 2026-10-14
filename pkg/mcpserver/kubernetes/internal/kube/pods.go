package kube

import (
	"fmt"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// PodSummary is one pod's line in a <resource>_pods result.
type PodSummary struct {
	Name       string    `json:"name"`
	Ready      int       `json:"ready"`
	Containers int       `json:"containers"`
	Status     string    `json:"status"`
	Restarts   int32     `json:"restarts"`
	Node       string    `json:"node,omitempty"`
	Created    time.Time `json:"created"`
	Revision   string    `json:"revision,omitempty"`
}

// SummarizePod counts native sidecars as containers, as kubectl get pods does; other init containers' restarts
// count only while the pod is still initializing.
func SummarizePod(p *corev1.Pod) PodSummary {
	s := PodSummary{
		Name:       p.Name,
		Containers: len(p.Spec.Containers),
		Status:     podStatus(p),
		Node:       p.Spec.NodeName,
		Created:    p.CreationTimestamp.Time,
	}
	for _, c := range p.Spec.InitContainers {
		if isRestartable(c) {
			s.Containers++
		}
	}
	_, initializing := initStatus(p)
	for _, cs := range p.Status.InitContainerStatuses {
		sidecar := isSidecar(p, cs.Name)
		if cs.Ready && sidecar {
			s.Ready++
		}
		if sidecar || initializing {
			s.Restarts += cs.RestartCount
		}
	}
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Ready {
			s.Ready++
		}
		s.Restarts += cs.RestartCount
	}
	return s
}

// podStatus follows kubectl get pods: a container's waiting or terminated reason wins over the pod phase.
func podStatus(p *corev1.Pod) string {
	if p.DeletionTimestamp != nil {
		if p.Status.Reason == "NodeLost" {
			return "Unknown"
		}
		return "Terminating"
	}
	reason := string(p.Status.Phase)
	if p.Status.Reason != "" {
		reason = p.Status.Reason
	}
	if init, ok := initStatus(p); ok {
		return init
	}
	// Walk backwards so the first container's reason wins, as kubectl does.
	for i := len(p.Status.ContainerStatuses) - 1; i >= 0; i-- {
		st := p.Status.ContainerStatuses[i].State
		switch {
		case st.Waiting != nil && st.Waiting.Reason != "":
			reason = st.Waiting.Reason
		case st.Terminated != nil:
			reason = terminatedReason(st.Terminated)
		}
	}
	return reason
}

// initStatus reports an unfinished init phase as Init:<reason> or Init:<done>/<total>.
func initStatus(p *corev1.Pod) (string, bool) {
	for i, cs := range p.Status.InitContainerStatuses {
		st := cs.State
		switch {
		case st.Terminated != nil && st.Terminated.ExitCode == 0:
			continue
		case cs.Started != nil && *cs.Started && isSidecar(p, cs.Name):
			continue
		case st.Terminated != nil:
			return "Init:" + terminatedReason(st.Terminated), true
		case st.Waiting != nil && st.Waiting.Reason != "" && st.Waiting.Reason != "PodInitializing":
			return "Init:" + st.Waiting.Reason, true
		default:
			return fmt.Sprintf("Init:%d/%d", i, len(p.Spec.InitContainers)), true
		}
	}
	return "", false
}

// isSidecar reports whether the named init container is a native sidecar.
func isSidecar(p *corev1.Pod, name string) bool {
	for _, c := range p.Spec.InitContainers {
		if c.Name == name {
			return isRestartable(c)
		}
	}
	return false
}

// isRestartable reports a native sidecar: an init container that keeps running alongside the main containers.
func isRestartable(c corev1.Container) bool {
	return c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways
}

func terminatedReason(t *corev1.ContainerStateTerminated) string {
	switch {
	case t.Reason != "":
		return t.Reason
	case t.Signal != 0:
		return fmt.Sprintf("Signal:%d", t.Signal)
	default:
		return fmt.Sprintf("ExitCode:%d", t.ExitCode)
	}
}

// formatPodTable renders pods like kubectl get pods, adding REVISION when any pod carries one.
func FormatPodTable(pods []PodSummary) string {
	header := []string{"NAME", "READY", "STATUS", "RESTARTS", "NODE", "AGE"}
	withRevision := false
	for _, p := range pods {
		withRevision = withRevision || p.Revision != ""
	}
	if withRevision {
		header = append(header, "REVISION")
	}
	rows := make([][]string, 0, len(pods))
	for _, p := range pods {
		row := []string{
			p.Name, fmt.Sprintf("%d/%d", p.Ready, p.Containers), p.Status,
			strconv.Itoa(int(p.Restarts)), OrNone(p.Node), Age(p.Created),
		}
		if withRevision {
			row = append(row, p.Revision)
		}
		rows = append(rows, row)
	}
	return Table(header, rows)
}
