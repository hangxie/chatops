package statefulset

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8s "k8s.io/client-go/kubernetes"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

// StatefulSetPodsOutput is the structured result of statefulset_pods: one entry per requested statefulset, in request order.
type StatefulSetPodsOutput struct {
	StatefulSets []kube.WorkloadPods `json:"statefulsets"`
}

// getPods marks each pod by its controller-revision-hash label against the statefulset's update revision.
func getPods(ctx context.Context, client k8s.Interface, ref kube.ObjectRef) (kube.WorkloadPods, error) {
	s, note, err := getStatefulSet(ctx, client, ref)
	if err != nil || s == nil {
		return kube.WorkloadPods{Namespace: ref.Namespace, Name: ref.Name, Note: note}, err
	}
	selector, err := metav1.LabelSelectorAsSelector(s.Spec.Selector)
	if err != nil {
		return kube.WorkloadPods{}, err
	}
	out, err := kube.OwnedPods(ctx, client, s.Namespace, selector, map[types.UID]bool{s.UID: true}, func(p *corev1.Pod, _ types.UID) string {
		return kube.Revision(p.Labels[appsv1.ControllerRevisionHashLabelKey], s.Status.UpdateRevision)
	})
	if err != nil {
		return kube.WorkloadPods{}, err
	}
	out.Namespace, out.Name, out.Health = ref.Namespace, ref.Name, health(s)
	return out, nil
}
