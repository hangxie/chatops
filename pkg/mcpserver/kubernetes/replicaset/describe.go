package replicaset

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8s "k8s.io/client-go/kubernetes"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

// ReplicaSetDescribeOutput is the structured result of replicaset_describe: one entry per requested replicaset, in request order.
type ReplicaSetDescribeOutput struct {
	ReplicaSets []kube.Described `json:"replicasets"`
}

func describe(ctx context.Context, client k8s.Interface, ref kube.ObjectRef) (kube.Described, error) {
	d, note, err := getReplicaSet(ctx, client, ref)
	if err != nil || d == nil {
		return kube.Described{Namespace: ref.Namespace, Name: ref.Name, Note: note}, err
	}
	h := health(d)
	d.TypeMeta = metav1.TypeMeta{APIVersion: "apps/v1", Kind: "ReplicaSet"}
	kube.CleanMeta(&d.ObjectMeta)
	kube.RedactPodSpec(&d.Spec.Template.Spec)
	text, err := kube.ToYAML(d)
	if err != nil {
		return kube.Described{}, err
	}
	return kube.Described{Namespace: ref.Namespace, Name: ref.Name, Found: true, Health: h, YAML: text}, nil
}
