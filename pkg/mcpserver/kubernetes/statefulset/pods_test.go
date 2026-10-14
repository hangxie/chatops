package statefulset

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
	s := statefulSet("db", "pg", withStatus(3, 1, 3, "pg-1", "pg-2"))
	other := statefulSet("db", "other", func(o *appsv1.StatefulSet) { o.Spec.Selector.MatchLabels["app"] = "pg" })
	client := fake.NewClientset(kubetest.Namespace("db"), s, other,
		kubetest.WorkloadPod("db", "pg-0", "pg", ownedBy(s), revision("pg-1")),
		kubetest.WorkloadPod("db", "pg-2", "pg", ownedBy(s), revision("pg-2")),
		kubetest.WorkloadPod("db", "other-0", "pg", ownedBy(other), revision("pg-2")),
	)

	out, err := getPods(context.Background(), client, kube.ObjectRef{Namespace: "db", Name: "pg"})
	require.NoError(t, err)
	require.Equal(t, healthProgressing, out.Health)
	require.Equal(t, "app=pg", out.Selector)
	revisions := map[string]string{}
	for _, p := range out.Pods {
		revisions[p.Name] = p.Revision
	}
	require.Equal(t, map[string]string{"pg-0": kube.RevisionOld, "pg-2": kube.RevisionCurrent}, revisions)
}

func Test_getPods_misses_and_errors(t *testing.T) {
	client := fake.NewClientset(kubetest.Namespace("db"))
	out, err := getPods(context.Background(), client, kube.ObjectRef{Namespace: "nope", Name: "pg"})
	require.NoError(t, err)
	require.Equal(t, kube.WorkloadPods{Namespace: "nope", Name: "pg", Note: `namespace "nope" does not exist`}, out)

	tests := map[string]struct {
		verb, resource string
		s              *appsv1.StatefulSet
	}{
		"get statefulset": {verb: "get", resource: "statefulsets", s: statefulSet("db", "pg")},
		"list pods":       {verb: "list", resource: "pods", s: statefulSet("db", "pg")},
		"bad selector": {verb: "none", resource: "none", s: statefulSet("db", "pg", func(s *appsv1.StatefulSet) {
			s.Spec.Selector.MatchExpressions = []metav1.LabelSelectorRequirement{{Key: "app", Operator: "Bogus"}}
		})},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			client := fake.NewClientset(tc.s)
			client.PrependReactor(tc.verb, tc.resource, kubetest.Forbidden(tc.verb, tc.resource))
			_, err := getPods(context.Background(), client, kube.ObjectRef{Namespace: "db", Name: "pg"})
			require.Error(t, err)
		})
	}
}
