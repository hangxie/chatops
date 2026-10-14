package deployment

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8s "k8s.io/client-go/kubernetes"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

// DeploymentDescribeOutput is the structured result of deployment_describe: one entry per requested deployment, in request order.
type DeploymentDescribeOutput struct {
	Deployments []kube.Described `json:"deployments"`
}

func describeDeployment(ctx context.Context, client k8s.Interface, ref kube.ObjectRef) (kube.Described, error) {
	d, note, err := getDeployment(ctx, client, ref)
	if err != nil || d == nil {
		return kube.Described{Namespace: ref.Namespace, Name: ref.Name, Note: note}, err
	}
	health := deploymentHealth(d)
	// Typed Get leaves TypeMeta empty; restore it so the YAML reads like kubectl get -o yaml.
	d.TypeMeta = metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"}
	kube.CleanMeta(&d.ObjectMeta)
	kube.RedactPodSpec(&d.Spec.Template.Spec)
	text, err := kube.ToYAML(d)
	if err != nil {
		return kube.Described{}, err
	}
	return kube.Described{Namespace: ref.Namespace, Name: ref.Name, Found: true, Health: health, YAML: text}, nil
}
