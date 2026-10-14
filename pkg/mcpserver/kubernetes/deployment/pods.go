package deployment

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

const revisionAnnotation = "deployment.kubernetes.io/revision"

// DeploymentPodsOutput is the structured result of deployment_pods: one entry per requested deployment, in request order.
type DeploymentPodsOutput struct {
	Deployments []kube.WorkloadPods `json:"deployments"`
}

// getDeploymentPods marks each pod by its owning ReplicaSet against the one at the deployment's revision.
func getDeploymentPods(ctx context.Context, client k8s.Interface, ref kube.ObjectRef) (kube.WorkloadPods, error) {
	d, note, err := getDeployment(ctx, client, ref)
	if err != nil || d == nil {
		return kube.WorkloadPods{Namespace: ref.Namespace, Name: ref.Name, Note: note}, err
	}
	selector, err := metav1.LabelSelectorAsSelector(d.Spec.Selector)
	if err != nil {
		return kube.WorkloadPods{}, err
	}
	owned, current, err := ownedReplicaSets(ctx, client, d, selector)
	if err != nil {
		return kube.WorkloadPods{}, err
	}
	out, err := kube.OwnedPods(ctx, client, d.Namespace, selector, owned, func(_ *corev1.Pod, owner types.UID) string {
		return kube.Revision(string(owner), string(current))
	})
	if err != nil {
		return kube.WorkloadPods{}, err
	}
	out.Namespace, out.Name, out.Health = ref.Namespace, ref.Name, deploymentHealth(d)
	return out, nil
}

// ownedReplicaSets returns the UIDs of the ReplicaSets d controls and the one at d's revision, empty when none
// matches yet.
func ownedReplicaSets(ctx context.Context, client k8s.Interface, d *appsv1.Deployment, selector labels.Selector) (map[types.UID]bool, types.UID, error) {
	sets, err := client.AppsV1().ReplicaSets(d.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return nil, "", err
	}
	revision := d.Annotations[revisionAnnotation]
	owned := map[types.UID]bool{}
	var current types.UID
	for i := range sets.Items {
		rs := &sets.Items[i]
		if !metav1.IsControlledBy(rs, d) {
			continue
		}
		owned[rs.UID] = true
		if revision != "" && rs.Annotations[revisionAnnotation] == revision {
			current = rs.UID
		}
	}
	return owned, current, nil
}
