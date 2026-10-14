package deployment

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

// replicaSet builds a ReplicaSet controlled by d at the given revision, labeled with d's selector.
func replicaSet(d *appsv1.Deployment, revision, hash string) *appsv1.ReplicaSet {
	labels := map[string]string{appsv1.DefaultDeploymentUniqueLabelKey: hash}
	for k, v := range d.Spec.Selector.MatchLabels {
		labels[k] = v
	}
	return &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Namespace: d.Namespace, Name: d.Name + "-" + hash, UID: types.UID(d.Name + "-" + hash), Labels: labels,
		Annotations:     map[string]string{revisionAnnotation: revision},
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(d, appsv1.SchemeGroupVersion.WithKind("Deployment"))},
	}}
}

func Test_getDeploymentPods(t *testing.T) {
	d := deployment("argocd", "server", func(d *appsv1.Deployment) { d.Annotations[revisionAnnotation] = "2" })
	oldRS, currentRS := replicaSet(d, "1", "aaa"), replicaSet(d, "2", "bbb")
	other := deployment("argocd", "other", func(d *appsv1.Deployment) { d.Spec.Selector.MatchLabels["app"] = "server" })
	foreignRS := replicaSet(other, "1", "zzz") // matches the selector but is controlled by another deployment
	client := fake.NewClientset(
		kubetest.Namespace("argocd"), d, oldRS, currentRS, foreignRS,
		kubetest.WorkloadPod("argocd", "server-bbb-1", "server", kubetest.OwnedBy(currentRS)),
		kubetest.WorkloadPod("argocd", "server-aaa-1", "server", kubetest.OwnedBy(oldRS), kubetest.WithContainer(kubetest.Waiting("CrashLoopBackOff"), false, 4)),
		kubetest.WorkloadPod("argocd", "server-stray", "server"),
		kubetest.WorkloadPod("argocd", "other-zzz-1", "server", kubetest.OwnedBy(foreignRS)),
		kubetest.WorkloadPod("argocd", "web-1", "web", kubetest.OwnedBy(currentRS)),
		kubetest.WorkloadPod("other", "server-x", "server", kubetest.OwnedBy(currentRS)),
	)

	out, err := getDeploymentPods(context.Background(), client, kube.ObjectRef{Namespace: "argocd", Name: "server"})
	require.NoError(t, err)
	require.True(t, out.Found)
	require.Equal(t, healthHealthy, out.Health)
	require.Equal(t, "app=server", out.Selector)
	revisions := map[string]string{}
	for _, p := range out.Pods {
		revisions[p.Name] = p.Revision
	}
	// Selector-matching pods without the deployment's ReplicaSet as controller are not its pods.
	require.Equal(t, map[string]string{"server-bbb-1": kube.RevisionCurrent, "server-aaa-1": kube.RevisionOld}, revisions)
}

func Test_getDeploymentPods_no_current_replicaset(t *testing.T) {
	atRevision2 := deployment("ns", "web", func(d *appsv1.Deployment) { d.Annotations[revisionAnnotation] = "2" })
	noRevision := deployment("ns", "web", func(d *appsv1.Deployment) { delete(d.Annotations, revisionAnnotation) })
	tests := map[string]struct {
		d *appsv1.Deployment
	}{
		"no replicaset at revision": {d: atRevision2},
		"no revision annotation":    {d: noRevision},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			rs := replicaSet(tc.d, "1", "aaa")
			objs := []runtime.Object{tc.d, rs, kubetest.WorkloadPod("ns", "web-1", "web", kubetest.OwnedBy(rs))}
			out, err := getDeploymentPods(context.Background(), fake.NewClientset(objs...), kube.ObjectRef{Namespace: "ns", Name: "web"})
			require.NoError(t, err)
			require.Len(t, out.Pods, 1)
			require.Equal(t, kube.RevisionUnknown, out.Pods[0].Revision)
		})
	}
}

func Test_getDeploymentPods_misses(t *testing.T) {
	client := fake.NewClientset(kubetest.Namespace("argocd"), deployment("argocd", "idle"))
	tests := map[string]struct {
		ref   kube.ObjectRef
		found bool
		note  string
	}{
		"no pods":           {ref: kube.ObjectRef{Namespace: "argocd", Name: "idle"}, found: true},
		"missing":           {ref: kube.ObjectRef{Namespace: "argocd", Name: "ghost"}},
		"missing namespace": {ref: kube.ObjectRef{Namespace: "nope", Name: "ghost"}, note: `namespace "nope" does not exist`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			out, err := getDeploymentPods(context.Background(), client, tc.ref)
			require.NoError(t, err)
			require.Equal(t, tc.ref.Namespace, out.Namespace)
			require.Equal(t, tc.ref.Name, out.Name)
			require.Equal(t, tc.found, out.Found)
			require.Equal(t, tc.note, out.Note)
			require.Empty(t, out.Pods)
		})
	}
}

func Test_getDeploymentPods_errors(t *testing.T) {
	tests := map[string]struct {
		verb, resource string
		deployment     *appsv1.Deployment
	}{
		"get deployment":   {verb: "get", resource: "deployments", deployment: deployment("ns", "web")},
		"list pods":        {verb: "list", resource: "pods", deployment: deployment("ns", "web")},
		"list replicasets": {verb: "list", resource: "replicasets", deployment: deployment("ns", "web")},
		"bad selector": {verb: "none", resource: "none", deployment: deployment("ns", "web", func(d *appsv1.Deployment) {
			d.Spec.Selector.MatchExpressions = []metav1.LabelSelectorRequirement{{Key: "app", Operator: "Bogus"}}
		})},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			client := fake.NewClientset(tc.deployment)
			client.PrependReactor(tc.verb, tc.resource, kubetest.Forbidden(tc.verb, tc.resource))
			_, err := getDeploymentPods(context.Background(), client, kube.ObjectRef{Namespace: "ns", Name: "web"})
			require.Error(t, err)
		})
	}
}
