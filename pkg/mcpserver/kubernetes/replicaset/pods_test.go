package replicaset

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

func Test_getPods(t *testing.T) {
	r := replicaSet("argocd", "web", withStatus(2, 1))
	other := replicaSet("argocd", "other", func(o *appsv1.ReplicaSet) { o.Spec.Selector.MatchLabels["app"] = "web" })
	client := fake.NewClientset(kubetest.Namespace("argocd"), r, other,
		kubetest.WorkloadPod("argocd", "web-1", "web", kubetest.OwnedBy(r)),
		kubetest.WorkloadPod("argocd", "other-1", "web", kubetest.OwnedBy(other)),
	)

	out, err := getPods(context.Background(), client, kube.ObjectRef{Namespace: "argocd", Name: "web"})
	require.NoError(t, err)
	require.Equal(t, healthDegraded, out.Health)
	require.Len(t, out.Pods, 1)
	require.Equal(t, "web-1", out.Pods[0].Name)
	require.Empty(t, out.Pods[0].Revision)
}

func Test_getPods_misses_and_errors(t *testing.T) {
	out, err := getPods(context.Background(), fake.NewClientset(), kube.ObjectRef{Namespace: "nope", Name: "x"})
	require.NoError(t, err)
	require.Equal(t, kube.WorkloadPods{Namespace: "nope", Name: "x", Note: `namespace "nope" does not exist`}, out)

	tests := map[string]struct {
		verb, resource string
		r              *appsv1.ReplicaSet
	}{
		"get replicaset": {verb: "get", resource: "replicasets", r: replicaSet("ns", "r")},
		"list pods":      {verb: "list", resource: "pods", r: replicaSet("ns", "r")},
		"bad selector": {verb: "none", resource: "none", r: replicaSet("ns", "r", func(r *appsv1.ReplicaSet) {
			r.Spec.Selector.MatchExpressions = []metav1.LabelSelectorRequirement{{Key: "app", Operator: "Bogus"}}
		})},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			client := fake.NewClientset(tc.r)
			client.PrependReactor(tc.verb, tc.resource, kubetest.Forbidden(tc.verb, tc.resource))
			_, err := getPods(context.Background(), client, kube.ObjectRef{Namespace: "ns", Name: "r"})
			require.Error(t, err)
		})
	}
}
