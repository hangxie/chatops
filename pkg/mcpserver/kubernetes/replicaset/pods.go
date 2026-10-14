package replicaset

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8s "k8s.io/client-go/kubernetes"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

// ReplicaSetPodsOutput is the structured result of replicaset_pods: one entry per requested replicaset, in request order.
type ReplicaSetPodsOutput struct {
	ReplicaSets []kube.WorkloadPods `json:"replicasets"`
}

// getPods lists the pods the replicaset controls; a replicaset is one revision, so pods carry none.
func getPods(ctx context.Context, client k8s.Interface, ref kube.ObjectRef) (kube.WorkloadPods, error) {
	r, note, err := getReplicaSet(ctx, client, ref)
	if err != nil || r == nil {
		return kube.WorkloadPods{Namespace: ref.Namespace, Name: ref.Name, Note: note}, err
	}
	selector, err := metav1.LabelSelectorAsSelector(r.Spec.Selector)
	if err != nil {
		return kube.WorkloadPods{}, err
	}
	out, err := kube.OwnedPods(ctx, client, r.Namespace, selector, map[types.UID]bool{r.UID: true}, nil)
	if err != nil {
		return kube.WorkloadPods{}, err
	}
	out.Namespace, out.Name, out.Health = ref.Namespace, ref.Name, health(r)
	return out, nil
}
