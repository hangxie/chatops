package daemonset

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	k8s "k8s.io/client-go/kubernetes"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

// DaemonSetPodsOutput is the structured result of daemonset_pods: one entry per requested daemonset, in request order.
type DaemonSetPodsOutput struct {
	DaemonSets []kube.WorkloadPods `json:"daemonsets"`
}

// getPods marks each pod by its controller-revision-hash label against the daemonset's newest revision.
func getPods(ctx context.Context, client k8s.Interface, ref kube.ObjectRef) (kube.WorkloadPods, error) {
	d, note, err := getDaemonSet(ctx, client, ref)
	if err != nil || d == nil {
		return kube.WorkloadPods{Namespace: ref.Namespace, Name: ref.Name, Note: note}, err
	}
	selector, err := metav1.LabelSelectorAsSelector(d.Spec.Selector)
	if err != nil {
		return kube.WorkloadPods{}, err
	}
	current, err := currentHash(ctx, client, d, selector)
	if err != nil {
		return kube.WorkloadPods{}, err
	}
	out, err := kube.OwnedPods(ctx, client, d.Namespace, selector, map[types.UID]bool{d.UID: true}, func(p *corev1.Pod, _ types.UID) string {
		return kube.Revision(p.Labels[appsv1.ControllerRevisionHashLabelKey], current)
	})
	if err != nil {
		return kube.WorkloadPods{}, err
	}
	out.Namespace, out.Name, out.Health = ref.Namespace, ref.Name, health(d)
	return out, nil
}

// currentHash is the hash label of the newest ControllerRevision the daemonset controls, since its status does
// not name the current revision; empty when it has none yet.
func currentHash(ctx context.Context, client k8s.Interface, d *appsv1.DaemonSet, selector labels.Selector) (string, error) {
	revisions, err := client.AppsV1().ControllerRevisions(d.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return "", err
	}
	var newest *appsv1.ControllerRevision
	for i := range revisions.Items {
		r := &revisions.Items[i]
		if metav1.IsControlledBy(r, d) && (newest == nil || r.Revision > newest.Revision) {
			newest = r
		}
	}
	if newest == nil {
		return "", nil
	}
	return newest.Labels[appsv1.ControllerRevisionHashLabelKey], nil
}
