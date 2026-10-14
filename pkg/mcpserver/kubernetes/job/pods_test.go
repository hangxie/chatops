package job

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

func Test_getPods(t *testing.T) {
	j := job("db", "migrate", condition(batchv1.JobFailed))
	other := job("db", "other", func(o *batchv1.Job) { o.Spec.Selector.MatchLabels["app"] = "migrate" })
	client := fake.NewClientset(kubetest.Namespace("db"), j, other,
		kubetest.WorkloadPod("db", "migrate-a", "migrate", ownedBy(j), kubetest.WithContainer(kubetest.Terminated("Error", 1, 0), false, 0)),
		kubetest.WorkloadPod("db", "other-a", "migrate", ownedBy(other)),
	)

	out, err := getPods(context.Background(), client, kube.ObjectRef{Namespace: "db", Name: "migrate"})
	require.NoError(t, err)
	require.Equal(t, healthFailed, out.Health)
	require.Len(t, out.Pods, 1)
	require.Equal(t, "Error", out.Pods[0].Status)
	require.Empty(t, out.Pods[0].Revision)
}

func Test_getPods_misses_and_errors(t *testing.T) {
	out, err := getPods(context.Background(), fake.NewClientset(), kube.ObjectRef{Namespace: "nope", Name: "x"})
	require.NoError(t, err)
	require.Equal(t, kube.WorkloadPods{Namespace: "nope", Name: "x", Note: `namespace "nope" does not exist`}, out)

	tests := map[string]struct {
		verb, resource string
		j              *batchv1.Job
	}{
		"get job":   {verb: "get", resource: "jobs", j: job("ns", "j")},
		"list pods": {verb: "list", resource: "pods", j: job("ns", "j")},
		"bad selector": {verb: "none", resource: "none", j: job("ns", "j", func(j *batchv1.Job) {
			j.Spec.Selector.MatchExpressions = []metav1.LabelSelectorRequirement{{Key: "app", Operator: "Bogus"}}
		})},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			client := fake.NewClientset(tc.j)
			client.PrependReactor(tc.verb, tc.resource, kubetest.Forbidden(tc.verb, tc.resource))
			_, err := getPods(context.Background(), client, kube.ObjectRef{Namespace: "ns", Name: "j"})
			require.Error(t, err)
		})
	}
}
