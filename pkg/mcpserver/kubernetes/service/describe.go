package service

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8s "k8s.io/client-go/kubernetes"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

// ServiceDescribeOutput is the structured result of service_describe: one entry per requested service, in request order.
type ServiceDescribeOutput struct {
	Services []kube.Described `json:"services"`
}

// describe takes health from the service's endpoints, as service_list does.
func describe(ctx context.Context, client k8s.Interface, ref kube.ObjectRef) (kube.Described, error) {
	svc, note, err := getService(ctx, client, ref)
	if err != nil || svc == nil {
		return kube.Described{Namespace: ref.Namespace, Name: ref.Name, Note: note}, err
	}
	slices, err := serviceSlices(ctx, client, ref)
	if err != nil {
		return kube.Described{}, err
	}
	ready, total := countEndpoints(mergeEndpoints(slices))
	h := health(svc, ready, total)
	svc.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}
	kube.CleanMeta(&svc.ObjectMeta)
	text, err := kube.ToYAML(svc)
	if err != nil {
		return kube.Described{}, err
	}
	return kube.Described{Namespace: ref.Namespace, Name: ref.Name, Found: true, Health: h, YAML: text}, nil
}
