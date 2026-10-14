package statefulset

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8s "k8s.io/client-go/kubernetes"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

// StatefulSetDescribeOutput is the structured result of statefulset_describe: one entry per requested statefulset, in request order.
type StatefulSetDescribeOutput struct {
	StatefulSets []kube.Described `json:"statefulsets"`
}

func describe(ctx context.Context, client k8s.Interface, ref kube.ObjectRef) (kube.Described, error) {
	s, note, err := getStatefulSet(ctx, client, ref)
	if err != nil || s == nil {
		return kube.Described{Namespace: ref.Namespace, Name: ref.Name, Note: note}, err
	}
	h := health(s)
	s.TypeMeta = metav1.TypeMeta{APIVersion: "apps/v1", Kind: "StatefulSet"}
	kube.CleanMeta(&s.ObjectMeta)
	kube.RedactPodSpec(&s.Spec.Template.Spec)
	text, err := kube.ToYAML(s)
	if err != nil {
		return kube.Described{}, err
	}
	return kube.Described{Namespace: ref.Namespace, Name: ref.Name, Found: true, Health: h, YAML: text}, nil
}
