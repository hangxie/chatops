package deployment

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	k8s "k8s.io/client-go/kubernetes"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

const revisionAnnotation = "deployment.kubernetes.io/revision"

// Pod revisions relative to the deployment's current ReplicaSet.
const (
	revisionCurrent = "current"
	revisionOld     = "old"
	revisionUnknown = "unknown"
)

// DeploymentPodsOutput is the structured result of deployment_pods: one entry per requested deployment, in request order.
type DeploymentPodsOutput struct {
	Deployments []DeploymentPods `json:"deployments"`
}

// DeploymentPods is one deployment's pods; detail fields are omitted on a miss.
type DeploymentPods struct {
	Namespace string            `json:"namespace"`
	Name      string            `json:"name"`
	Found     bool              `json:"found"`
	Note      string            `json:"note,omitempty"`
	Health    string            `json:"health,omitempty"`
	Selector  string            `json:"selector,omitempty"`
	Pods      []kube.PodSummary `json:"pods,omitempty"`
}

func getDeploymentPods(ctx context.Context, client k8s.Interface, ref kube.ObjectRef) (DeploymentPods, error) {
	d, note, err := getDeployment(ctx, client, ref)
	if err != nil || d == nil {
		return DeploymentPods{Namespace: ref.Namespace, Name: ref.Name, Note: note}, err
	}
	selector, err := metav1.LabelSelectorAsSelector(d.Spec.Selector)
	if err != nil {
		return DeploymentPods{}, err
	}
	owned, current, err := ownedReplicaSets(ctx, client, d, selector)
	if err != nil {
		return DeploymentPods{}, err
	}
	pods, err := client.CoreV1().Pods(d.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return DeploymentPods{}, err
	}
	out := DeploymentPods{
		Namespace: ref.Namespace,
		Name:      ref.Name,
		Found:     true,
		Health:    deploymentHealth(d),
		Selector:  selector.String(),
		Pods:      make([]kube.PodSummary, 0, len(pods.Items)),
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		owner := metav1.GetControllerOf(p)
		// A selector match alone is not ownership: another controller's pods can carry the same labels.
		if owner == nil || !owned[owner.UID] {
			continue
		}
		s := kube.SummarizePod(p)
		s.Revision = podRevision(owner.UID, current)
		out.Pods = append(out.Pods, s)
	}
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

func podRevision(owner, current types.UID) string {
	switch {
	case current == "":
		return revisionUnknown
	case owner == current:
		return revisionCurrent
	default:
		return revisionOld
	}
}
