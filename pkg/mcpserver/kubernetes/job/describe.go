package job

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8s "k8s.io/client-go/kubernetes"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

// JobDescribeOutput is the structured result of job_describe: one entry per requested job, in request order.
type JobDescribeOutput struct {
	Jobs []kube.Described `json:"jobs"`
}

func describe(ctx context.Context, client k8s.Interface, ref kube.ObjectRef) (kube.Described, error) {
	d, note, err := getJob(ctx, client, ref)
	if err != nil || d == nil {
		return kube.Described{Namespace: ref.Namespace, Name: ref.Name, Note: note}, err
	}
	h := health(d)
	d.TypeMeta = metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"}
	kube.CleanMeta(&d.ObjectMeta)
	kube.RedactPodSpec(&d.Spec.Template.Spec)
	text, err := kube.ToYAML(d)
	if err != nil {
		return kube.Described{}, err
	}
	return kube.Described{Namespace: ref.Namespace, Name: ref.Name, Found: true, Health: h, YAML: text}, nil
}
