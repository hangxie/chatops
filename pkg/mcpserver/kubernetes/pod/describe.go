package pod

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8s "k8s.io/client-go/kubernetes"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

// PodDescribeOutput is the structured result of pod_describe: one entry per requested pod, in request order.
type PodDescribeOutput struct {
	Pods []kube.Described `json:"pods"`
}

// describePod uses the kubectl-style pod status as its health.
func describePod(ctx context.Context, client k8s.Interface, ref kube.ObjectRef) (kube.Described, error) {
	p, note, err := getPod(ctx, client, ref)
	if err != nil || p == nil {
		return kube.Described{Namespace: ref.Namespace, Name: ref.Name, Note: note}, err
	}
	status := kube.SummarizePod(p).Status
	p.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}
	kube.CleanMeta(&p.ObjectMeta)
	kube.RedactPodSpec(&p.Spec)
	text, err := kube.ToYAML(p)
	if err != nil {
		return kube.Described{}, err
	}
	return kube.Described{Namespace: ref.Namespace, Name: ref.Name, Found: true, Health: status, YAML: text}, nil
}
