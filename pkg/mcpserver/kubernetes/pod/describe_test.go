package pod

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

func Test_describePod(t *testing.T) {
	p := kubetest.WorkloadPod("argocd", "server-1", "server", kubetest.WithContainer(kubetest.Waiting("CrashLoopBackOff"), false, 4),
		func(p *corev1.Pod) {
			p.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "kubelet"}}
			p.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "TOKEN", Value: "sk-0123456789abcdefx7Q"}}
		})
	client := fake.NewClientset(kubetest.Namespace("argocd"), p)

	out, err := describePod(context.Background(), client, kube.ObjectRef{Namespace: "argocd", Name: "server-1"})
	require.NoError(t, err)
	require.True(t, out.Found)
	require.Equal(t, "CrashLoopBackOff", out.Health)
	require.Contains(t, out.YAML, "apiVersion: v1\nkind: Pod\n")
	require.Contains(t, out.YAML, "value: <redacted …x7Q>")
	require.Contains(t, out.YAML, "restartCount: 4")
	require.NotContains(t, out.YAML, "managedFields")

	out, err = describePod(context.Background(), client, kube.ObjectRef{Namespace: "nope", Name: "x"})
	require.NoError(t, err)
	require.Equal(t, kube.Described{Namespace: "nope", Name: "x", Note: `namespace "nope" does not exist`}, out)
}

func Test_describePod_error(t *testing.T) {
	client := fake.NewClientset()
	client.PrependReactor("get", "pods", kubetest.Forbidden("get", "pods"))
	_, err := describePod(context.Background(), client, kube.ObjectRef{Namespace: "argocd", Name: "x"})
	require.ErrorContains(t, err, "get denied")
}
