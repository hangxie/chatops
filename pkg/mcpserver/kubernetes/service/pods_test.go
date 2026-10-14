package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

func Test_getPods(t *testing.T) {
	manual := discoveryv1.Endpoint{Addresses: []string{"192.168.1.50"}} // not a pod, no conditions
	client := fake.NewClientset(kubetest.Namespace("argocd"), service("argocd", "server"),
		slice("argocd", "server", "server-v4", ep("server-1", true, false, "10.1.0.1"), ep("server-2", false, true, "10.1.0.2"), manual),
		slice("argocd", "server", "server-v6", ep("server-1", true, false, "fd00::1")),
		slice("argocd", "other", "other-a", ep("other-1", true, false)),
	)

	out, err := getPods(context.Background(), client, kube.ObjectRef{Namespace: "argocd", Name: "server"})
	require.NoError(t, err)
	require.Equal(t, ServicePods{Namespace: "argocd", Name: "server", Found: true, Health: healthHealthy, Type: "ClusterIP", Endpoints: []Endpoint{
		{Pod: "server-1", Addresses: []string{"10.1.0.1", "fd00::1"}, Ready: true, Serving: true, Node: "worker-0"},
		{Pod: "server-2", Addresses: []string{"10.1.0.2"}, Serving: true, Terminating: true, Node: "worker-0"},
		{Addresses: []string{"192.168.1.50"}, Ready: true, Serving: true},
	}}, out)
}

func Test_getPods_misses_and_errors(t *testing.T) {
	out, err := getPods(context.Background(), fake.NewClientset(), kube.ObjectRef{Namespace: "nope", Name: "x"})
	require.NoError(t, err)
	require.Equal(t, ServicePods{Namespace: "nope", Name: "x", Note: `namespace "nope" does not exist`}, out)

	for _, tc := range []struct{ verb, resource string }{{"get", "services"}, {"list", "endpointslices"}} {
		t.Run(tc.resource, func(t *testing.T) {
			client := fake.NewClientset(service("ns", "web"))
			client.PrependReactor(tc.verb, tc.resource, kubetest.Forbidden(tc.verb, tc.resource))
			_, err := getPods(context.Background(), client, kube.ObjectRef{Namespace: "ns", Name: "web"})
			require.Error(t, err)
		})
	}
}

func Test_formatPods(t *testing.T) {
	out := ServicePods{Namespace: "argocd", Name: "server", Found: true, Health: healthDegraded, Type: "ClusterIP", Endpoints: []Endpoint{
		{Pod: "server-1", Addresses: []string{"10.1.0.1", "fd00::1"}, Ready: true, Serving: true, Node: "worker-0"},
		{Addresses: []string{"192.168.1.50"}},
	}}
	text := formatPods(out)
	require.Contains(t, text, "argocd/server Degraded (ClusterIP)\n")
	require.Regexp(t, `POD\s+ADDRESS\s+READY\s+SERVING\s+TERMINATING\s+NODE`, text)
	require.Regexp(t, `server-1\s+10\.1\.0\.1,fd00::1\s+yes\s+yes\s+no\s+worker-0`, text)
	require.Regexp(t, `n/a\s+192\.168\.1\.50\s+no\s+no\s+no\s+n/a`, text)

	require.Equal(t, "argocd/db External (ExternalName)\nno endpoints",
		formatPods(ServicePods{Namespace: "argocd", Name: "db", Found: true, Health: healthExternal, Type: string(corev1.ServiceTypeExternalName)}))
	require.Equal(t, `service "argocd/ghost" not found`, formatPods(ServicePods{Namespace: "argocd", Name: "ghost"}))
}
