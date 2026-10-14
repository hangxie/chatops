package job

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8s "k8s.io/client-go/kubernetes"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

// JobPodsOutput is the structured result of job_pods: one entry per requested job, in request order.
type JobPodsOutput struct {
	Jobs []kube.WorkloadPods `json:"jobs"`
}

// getPods lists the pods the job controls, one per attempt; a job has no revisions.
func getPods(ctx context.Context, client k8s.Interface, ref kube.ObjectRef) (kube.WorkloadPods, error) {
	j, note, err := getJob(ctx, client, ref)
	if err != nil || j == nil {
		return kube.WorkloadPods{Namespace: ref.Namespace, Name: ref.Name, Note: note}, err
	}
	selector, err := metav1.LabelSelectorAsSelector(j.Spec.Selector)
	if err != nil {
		return kube.WorkloadPods{}, err
	}
	out, err := kube.OwnedPods(ctx, client, j.Namespace, selector, map[types.UID]bool{j.UID: true}, nil)
	if err != nil {
		return kube.WorkloadPods{}, err
	}
	out.Namespace, out.Name, out.Health = ref.Namespace, ref.Name, health(j)
	return out, nil
}
