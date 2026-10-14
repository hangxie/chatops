package daemonset

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

func Test_describe(t *testing.T) {
	d := daemonSet("kube-system", "cilium", func(s *appsv1.DaemonSet) {
		s.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "helm"}}
		s.Spec.Template.Spec.Containers = []corev1.Container{{Name: "agent", Image: "cilium:v1.18", Env: []corev1.EnvVar{
			{Name: "API_TOKEN", Value: "hunter2"},
		}}}
	})
	client := fake.NewClientset(kubetest.Namespace("kube-system"), d)

	out, err := describe(context.Background(), client, kube.ObjectRef{Namespace: "kube-system", Name: "cilium"})
	require.NoError(t, err)
	require.True(t, out.Found)
	require.Equal(t, healthHealthy, out.Health)
	require.Contains(t, out.YAML, "apiVersion: apps/v1\nkind: DaemonSet\n")
	require.Contains(t, out.YAML, "value: <redacted>")
	require.NotContains(t, out.YAML, "hunter2")
	require.NotContains(t, out.YAML, "managedFields")

	out, err = describe(context.Background(), client, kube.ObjectRef{Namespace: "kube-system", Name: "ghost"})
	require.NoError(t, err)
	require.Equal(t, kube.Described{Namespace: "kube-system", Name: "ghost"}, out)

	client.PrependReactor("get", "daemonsets", kubetest.Forbidden("get", "daemonsets"))
	_, err = describe(context.Background(), client, kube.ObjectRef{Namespace: "kube-system", Name: "cilium"})
	require.ErrorContains(t, err, "get denied")
}
