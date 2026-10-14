package deployment

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

func Test_describeDeployment(t *testing.T) {
	d := deployment("argocd", "server", func(d *appsv1.Deployment) {
		d.ResourceVersion = "42"
		d.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "helm"}}
		d.Annotations["kubectl.kubernetes.io/last-applied-configuration"] = `{"spec":{}}`
		d.Spec.Template.Spec.Containers = []corev1.Container{{Name: "main", Image: "argocd:v3", Env: []corev1.EnvVar{
			{Name: "API_KEY", Value: "sk-0123456789abcdefx7Q"},
		}}}
	}, withStatus(1, 2, 2, 1, 1))
	client := fake.NewClientset(kubetest.Namespace("argocd"), d)

	out, err := describeDeployment(context.Background(), client, kube.ObjectRef{Namespace: "argocd", Name: "server"})
	require.NoError(t, err)
	require.True(t, out.Found)
	require.Equal(t, healthDegraded, out.Health)
	require.Contains(t, out.YAML, "apiVersion: apps/v1\nkind: Deployment\n")
	require.Contains(t, out.YAML, "image: argocd:v3")
	require.Contains(t, out.YAML, "value: <redacted …x7Q>")
	require.Contains(t, out.YAML, "availableReplicas: 1")
	for _, gone := range []string{"sk-0123456789", "managedFields", "last-applied-configuration", "resourceVersion", "uid:"} {
		require.NotContains(t, out.YAML, gone)
	}
}

func Test_describeDeployment_misses(t *testing.T) {
	client := fake.NewClientset(kubetest.Namespace("argocd"))
	tests := map[string]struct {
		ref  kube.ObjectRef
		note string
	}{
		"missing":           {ref: kube.ObjectRef{Namespace: "argocd", Name: "ghost"}},
		"missing namespace": {ref: kube.ObjectRef{Namespace: "nope", Name: "ghost"}, note: `namespace "nope" does not exist`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			out, err := describeDeployment(context.Background(), client, tc.ref)
			require.NoError(t, err)
			require.Equal(t, DeploymentDescribe{Namespace: tc.ref.Namespace, Name: tc.ref.Name, Note: tc.note}, out)
		})
	}
}

func Test_describeDeployment_error(t *testing.T) {
	client := fake.NewClientset()
	client.PrependReactor("get", "deployments", kubetest.Forbidden("get", "deployments"))
	_, err := describeDeployment(context.Background(), client, kube.ObjectRef{Namespace: "argocd", Name: "server"})
	require.ErrorContains(t, err, "get denied")
}
