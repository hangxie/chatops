package kube

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	k8s "k8s.io/client-go/kubernetes"
)

// Pod revisions relative to a workload's current revision.
const (
	RevisionCurrent = "current"
	RevisionOld     = "old"
	RevisionUnknown = "unknown"
)

// WorkloadPods is one workload's owned pods; detail fields are omitted on a miss.
type WorkloadPods struct {
	Namespace string       `json:"namespace"`
	Name      string       `json:"name"`
	Found     bool         `json:"found"`
	Note      string       `json:"note,omitempty"`
	Health    string       `json:"health,omitempty"`
	Selector  string       `json:"selector,omitempty"`
	Pods      []PodSummary `json:"pods,omitempty"`
}

// OwnedPods lists the pods selector matches whose controller is one of owners, since a selector match alone is
// not ownership; revision marks each pod against the workload's current revision, or is nil when it has none.
func OwnedPods(ctx context.Context, client k8s.Interface, namespace string, selector labels.Selector,
	owners map[types.UID]bool, revision func(p *corev1.Pod, owner types.UID) string,
) (WorkloadPods, error) {
	pods, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return WorkloadPods{}, err
	}
	out := WorkloadPods{Found: true, Selector: selector.String(), Pods: make([]PodSummary, 0, len(pods.Items))}
	for i := range pods.Items {
		p := &pods.Items[i]
		owner := metav1.GetControllerOf(p)
		if owner == nil || !owners[owner.UID] {
			continue
		}
		s := SummarizePod(p)
		if revision != nil {
			s.Revision = revision(p, owner.UID)
		}
		out.Pods = append(out.Pods, s)
	}
	return out, nil
}

// Revision marks a pod's revision against the current one; unknown while the current one is not known yet.
func Revision(pod, current string) string {
	switch {
	case current == "":
		return RevisionUnknown
	case pod == current:
		return RevisionCurrent
	default:
		return RevisionOld
	}
}

// FormatWorkloadPods renders a workload's pods under a header naming it, its health, and its selector.
func FormatWorkloadPods(kind string) func(WorkloadPods) string {
	return func(w WorkloadPods) string {
		target := w.Namespace + "/" + w.Name
		if !w.Found {
			return Miss(kind, target, w.Note)
		}
		head := fmt.Sprintf("%s %s (selector %s)", target, w.Health, w.Selector)
		if len(w.Pods) == 0 {
			return head + "\nno pods owned by this " + kind
		}
		return head + "\n" + FormatPodTable(w.Pods)
	}
}

// WorkloadListItem is one replicated workload's roster entry.
type WorkloadListItem struct {
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	Health    string    `json:"health"`
	Desired   int32     `json:"desired"`
	Ready     int32     `json:"ready"`
	Updated   int32     `json:"updated"`
	Available int32     `json:"available"`
	Created   time.Time `json:"created"`
}

// FormatWorkloadList renders workloads like kubectl get deployments, with the derived health; plural names the
// kind in the empty message.
func FormatWorkloadList(plural, namespace string, items []WorkloadListItem) string {
	if len(items) == 0 {
		if namespace != "" {
			return fmt.Sprintf("no %s in namespace %q", plural, namespace)
		}
		return "no " + plural + " found"
	}
	rows := make([][]string, 0, len(items))
	for _, w := range items {
		rows = append(rows, []string{
			w.Namespace, w.Name, w.Health, fmt.Sprintf("%d/%d", w.Ready, w.Desired),
			strconv.Itoa(int(w.Updated)), strconv.Itoa(int(w.Available)), Age(w.Created),
		})
	}
	return Table([]string{"NAMESPACE", "NAME", "HEALTH", "READY", "UP-TO-DATE", "AVAILABLE", "AGE"}, rows)
}

// CheckNamespace turns an empty list in a named namespace into an error when that namespace does not exist.
func CheckNamespace(ctx context.Context, client k8s.Interface, namespace string, n int) error {
	if n > 0 || namespace == "" {
		return nil
	}
	note, err := MissingNamespace(ctx, client, namespace)
	if err != nil {
		return err
	}
	if note != "" {
		return errors.New(note)
	}
	return nil
}
