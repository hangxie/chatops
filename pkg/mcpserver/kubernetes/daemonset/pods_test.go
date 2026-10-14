package daemonset

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

func Test_getPods(t *testing.T) {
	d := daemonSet("kube-system", "cilium", withStatus(3, 1, 3))
	other := daemonSet("kube-system", "other", func(o *appsv1.DaemonSet) { o.Spec.Selector.MatchLabels["app"] = "cilium" })
	client := fake.NewClientset(kubetest.Namespace("kube-system"), d, other,
		controllerRevision(d, 1, "old"), controllerRevision(d, 2, "new"), controllerRevision(other, 9, "foreign"),
		kubetest.WorkloadPod("kube-system", "cilium-a", "cilium", ownedBy(d), revision("old")),
		kubetest.WorkloadPod("kube-system", "cilium-b", "cilium", ownedBy(d), revision("new")),
		kubetest.WorkloadPod("kube-system", "other-a", "cilium", ownedBy(other), revision("foreign")),
	)

	out, err := getPods(context.Background(), client, kube.ObjectRef{Namespace: "kube-system", Name: "cilium"})
	require.NoError(t, err)
	require.Equal(t, healthProgressing, out.Health)
	revisions := map[string]string{}
	for _, p := range out.Pods {
		revisions[p.Name] = p.Revision
	}
	// The newest revision the daemonset itself controls is current; another daemonset's newer one is ignored.
	require.Equal(t, map[string]string{"cilium-a": kube.RevisionOld, "cilium-b": kube.RevisionCurrent}, revisions)
}

func Test_getPods_no_revision(t *testing.T) {
	d := daemonSet("kube-system", "cilium")
	objs := []runtime.Object{d, kubetest.WorkloadPod("kube-system", "cilium-a", "cilium", ownedBy(d), revision("abc"))}
	out, err := getPods(context.Background(), fake.NewClientset(objs...), kube.ObjectRef{Namespace: "kube-system", Name: "cilium"})
	require.NoError(t, err)
	require.Equal(t, kube.RevisionUnknown, out.Pods[0].Revision)
}

func Test_getPods_misses_and_errors(t *testing.T) {
	out, err := getPods(context.Background(), fake.NewClientset(), kube.ObjectRef{Namespace: "nope", Name: "x"})
	require.NoError(t, err)
	require.Equal(t, kube.WorkloadPods{Namespace: "nope", Name: "x", Note: `namespace "nope" does not exist`}, out)

	tests := map[string]struct {
		verb, resource string
		d              *appsv1.DaemonSet
	}{
		"get daemonset":  {verb: "get", resource: "daemonsets", d: daemonSet("ns", "d")},
		"list revisions": {verb: "list", resource: "controllerrevisions", d: daemonSet("ns", "d")},
		"list pods":      {verb: "list", resource: "pods", d: daemonSet("ns", "d")},
		"bad selector": {verb: "none", resource: "none", d: daemonSet("ns", "d", func(d *appsv1.DaemonSet) {
			d.Spec.Selector.MatchExpressions = []metav1.LabelSelectorRequirement{{Key: "app", Operator: "Bogus"}}
		})},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			client := fake.NewClientset(tc.d)
			client.PrependReactor(tc.verb, tc.resource, kubetest.Forbidden(tc.verb, tc.resource))
			_, err := getPods(context.Background(), client, kube.ObjectRef{Namespace: "ns", Name: "d"})
			require.Error(t, err)
		})
	}
}
