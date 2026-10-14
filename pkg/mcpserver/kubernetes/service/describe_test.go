package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

func Test_describe(t *testing.T) {
	client := fake.NewClientset(kubetest.Namespace("argocd"), service("argocd", "server"),
		slice("argocd", "server", "server-a", ep("server-1", false, false)))

	out, err := describe(context.Background(), client, kube.ObjectRef{Namespace: "argocd", Name: "server"})
	require.NoError(t, err)
	require.True(t, out.Found)
	require.Equal(t, healthUnavailable, out.Health)
	require.Contains(t, out.YAML, "apiVersion: v1\nkind: Service\n")
	require.Contains(t, out.YAML, "clusterIP: 10.0.0.1")

	out, err = describe(context.Background(), client, kube.ObjectRef{Namespace: "argocd", Name: "ghost"})
	require.NoError(t, err)
	require.Equal(t, kube.Described{Namespace: "argocd", Name: "ghost"}, out)

	for _, tc := range []struct{ verb, resource string }{{"get", "services"}, {"list", "endpointslices"}} {
		t.Run(tc.resource, func(t *testing.T) {
			client := fake.NewClientset(service("argocd", "server"))
			client.PrependReactor(tc.verb, tc.resource, kubetest.Forbidden(tc.verb, tc.resource))
			_, err := describe(context.Background(), client, kube.ObjectRef{Namespace: "argocd", Name: "server"})
			require.Error(t, err)
		})
	}
}
