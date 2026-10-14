package statefulset

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
	s := statefulSet("db", "pg", func(s *appsv1.StatefulSet) {
		s.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "helm"}}
		s.Spec.Template.Spec.Containers = []corev1.Container{{Name: "pg", Image: "postgres:17", Env: []corev1.EnvVar{
			{Name: "POSTGRES_PASSWORD", Value: "hunter2"},
		}}}
	})
	client := fake.NewClientset(kubetest.Namespace("db"), s)

	out, err := describe(context.Background(), client, kube.ObjectRef{Namespace: "db", Name: "pg"})
	require.NoError(t, err)
	require.True(t, out.Found)
	require.Equal(t, healthHealthy, out.Health)
	require.Contains(t, out.YAML, "apiVersion: apps/v1\nkind: StatefulSet\n")
	require.Contains(t, out.YAML, "value: <redacted>")
	require.NotContains(t, out.YAML, "hunter2")
	require.NotContains(t, out.YAML, "managedFields")

	out, err = describe(context.Background(), client, kube.ObjectRef{Namespace: "db", Name: "ghost"})
	require.NoError(t, err)
	require.Equal(t, kube.Described{Namespace: "db", Name: "ghost"}, out)

	client.PrependReactor("get", "statefulsets", kubetest.Forbidden("get", "statefulsets"))
	_, err = describe(context.Background(), client, kube.ObjectRef{Namespace: "db", Name: "pg"})
	require.ErrorContains(t, err, "get denied")
}
