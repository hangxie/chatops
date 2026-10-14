package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

func service(namespace, name string, opts ...func(*corev1.Service)) *corev1.Service {
	s := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.0.0.1",
			Selector: map[string]string{"app": name}, Ports: []corev1.ServicePort{{Port: 80, Protocol: corev1.ProtocolTCP}},
		},
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// ep builds an endpoint for pod name; ready and terminating are its conditions.
func ep(pod string, ready, terminating bool, addresses ...string) discoveryv1.Endpoint {
	if len(addresses) == 0 {
		addresses = []string{"10.1.0." + fmt.Sprint(len(pod))}
	}
	return discoveryv1.Endpoint{
		Addresses:  addresses,
		Conditions: discoveryv1.EndpointConditions{Ready: kubetest.Ptr(ready), Serving: kubetest.Ptr(ready || terminating), Terminating: kubetest.Ptr(terminating)},
		TargetRef:  &corev1.ObjectReference{Kind: "Pod", Name: pod, UID: types.UID("uid-" + pod)},
		NodeName:   kubetest.Ptr("worker-0"),
	}
}

func slice(namespace, svc, name string, endpoints ...discoveryv1.Endpoint) *discoveryv1.EndpointSlice {
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: map[string]string{discoveryv1.LabelServiceName: svc}},
		Endpoints:  endpoints,
	}
}

func Test_mergeEndpoints_and_count(t *testing.T) {
	unset := discoveryv1.Endpoint{Addresses: []string{"10.9.9.9"}} // no conditions: ready, not terminating
	tests := map[string]struct {
		slices       []discoveryv1.EndpointSlice
		ready, total int
	}{
		"dual stack counted once": {slices: []discoveryv1.EndpointSlice{
			*slice("ns", "web", "web-v4", ep("a", true, false), ep("b", false, false), ep("c", false, true), unset),
			*slice("ns", "web", "web-v6", ep("a", true, false, "fd00::1")),
		}, ready: 2, total: 3},
		"not ready in either family": {slices: []discoveryv1.EndpointSlice{
			*slice("ns", "web", "web-v4", ep("a", true, false, "10.1.0.1")),
			*slice("ns", "web", "web-v6", ep("a", false, false, "fd00::1")),
		}, ready: 0, total: 1},
		"terminating in either family": {slices: []discoveryv1.EndpointSlice{
			*slice("ns", "web", "web-v4", ep("a", true, false, "10.1.0.1")),
			*slice("ns", "web", "web-v6", ep("a", false, true, "fd00::1")),
		}, ready: 0, total: 0},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ready, total := countEndpoints(mergeEndpoints(tc.slices))
			require.Equal(t, tc.ready, ready)
			require.Equal(t, tc.total, total)
		})
	}

	// The conservative result does not depend on which slice is listed first.
	v4, v6 := slice("ns", "web", "web-v4", ep("a", true, false, "10.1.0.1")), slice("ns", "web", "web-v6", ep("a", false, false, "fd00::1"))
	for _, order := range [][]discoveryv1.EndpointSlice{{*v4, *v6}, {*v6, *v4}} {
		merged := mergeEndpoints(order)
		require.Len(t, merged, 1)
		require.False(t, merged[0].Ready)
		require.ElementsMatch(t, []string{"10.1.0.1", "fd00::1"}, merged[0].Addresses)
	}
	require.Equal(t, []string{"10.1.0.1"}, v4.Endpoints[0].Addresses, "merging must not touch the source slices")

	// A node known from only one family's entry is kept.
	noNode := ep("a", true, false, "10.1.0.1")
	noNode.NodeName = nil
	merged := mergeEndpoints([]discoveryv1.EndpointSlice{*slice("ns", "web", "v4", noNode), *slice("ns", "web", "v6", ep("a", true, false, "fd00::1"))})
	require.Equal(t, "worker-0", merged[0].Node)
}

func Test_health(t *testing.T) {
	external := service("ns", "x", func(s *corev1.Service) { s.Spec.Type = corev1.ServiceTypeExternalName })
	tests := map[string]struct {
		svc          *corev1.Service
		ready, total int
		want         string
	}{
		"external":     {svc: external, want: healthExternal},
		"no endpoints": {svc: service("ns", "web"), want: healthNoEndpoints},
		"unavailable":  {svc: service("ns", "web"), ready: 0, total: 2, want: healthUnavailable},
		"degraded":     {svc: service("ns", "web"), ready: 1, total: 2, want: healthDegraded},
		"healthy":      {svc: service("ns", "web"), ready: 2, total: 2, want: healthHealthy},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, health(tc.svc, tc.ready, tc.total))
		})
	}
}

func Test_externalIP_and_ports(t *testing.T) {
	tests := map[string]struct {
		svc   *corev1.Service
		ip    string
		ports string
	}{
		"cluster ip": {svc: service("ns", "web"), ip: "", ports: "80/TCP"},
		"load balancer": {svc: service("ns", "web", func(s *corev1.Service) {
			s.Spec.Type = corev1.ServiceTypeLoadBalancer
			s.Spec.Ports[0].NodePort = 30080
			s.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "192.168.1.10"}, {Hostname: "lb.example.com"}}
		}), ip: "192.168.1.10,lb.example.com", ports: "80:30080/TCP"},
		"pending load balancer": {svc: service("ns", "web", func(s *corev1.Service) { s.Spec.Type = corev1.ServiceTypeLoadBalancer }), ip: "<pending>", ports: "80/TCP"},
		"external ips":          {svc: service("ns", "web", func(s *corev1.Service) { s.Spec.ExternalIPs = []string{"1.2.3.4"} }), ip: "1.2.3.4", ports: "80/TCP"},
		"external name": {svc: service("ns", "web", func(s *corev1.Service) {
			s.Spec.Type, s.Spec.ExternalName, s.Spec.Ports = corev1.ServiceTypeExternalName, "db.example.com", nil
		}), ip: "db.example.com", ports: ""},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.ip, externalIP(tc.svc))
			require.Equal(t, tc.ports, ports(tc.svc))
		})
	}
}

func Test_listServices(t *testing.T) {
	created := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	client := fake.NewClientset(kubetest.Namespace("argocd"), kubetest.Namespace("empty"),
		service("argocd", "server", func(s *corev1.Service) { s.CreationTimestamp = metav1.NewTime(created) }),
		service("argocd", "repo"),
		service("other", "server"),
		slice("argocd", "server", "server-a", ep("server-1", true, false), ep("server-2", false, false)),
		slice("other", "server", "server-b", ep("x", true, false)),
	)
	tests := map[string]struct {
		namespace string
		want      []string
		errMsg    string
	}{
		"one namespace":     {namespace: "argocd", want: []string{"argocd/server", "argocd/repo"}},
		"every namespace":   {want: []string{"argocd/server", "argocd/repo", "other/server"}},
		"empty namespace":   {namespace: "empty", want: []string{}},
		"missing namespace": {namespace: "nope", errMsg: `namespace "nope" does not exist`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			out, err := listServices(context.Background(), client, tc.namespace)
			if tc.errMsg != "" {
				require.EqualError(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			got := []string{}
			for _, s := range out.Services {
				got = append(got, s.Namespace+"/"+s.Name)
			}
			require.ElementsMatch(t, tc.want, got)
		})
	}

	out, err := listServices(context.Background(), client, "")
	require.NoError(t, err)
	// Same-named services in two namespaces keep their own endpoints.
	require.Contains(t, out.Services, ServiceListItem{
		Namespace: "argocd", Name: "server", Type: "ClusterIP", ClusterIP: "10.0.0.1",
		Ports: "80/TCP", Health: healthDegraded, Ready: 1, Endpoints: 2, Created: created,
	})
	require.Contains(t, out.Services, ServiceListItem{
		Namespace: "other", Name: "server", Type: "ClusterIP", ClusterIP: "10.0.0.1",
		Ports: "80/TCP", Health: healthHealthy, Ready: 1, Endpoints: 1,
	})
}

func Test_listServices_errors(t *testing.T) {
	for _, resource := range []string{"services", "endpointslices"} {
		t.Run(resource, func(t *testing.T) {
			client := fake.NewClientset(service("argocd", "server"))
			client.PrependReactor("list", resource, kubetest.Forbidden("list", resource))
			_, err := listServices(context.Background(), client, "argocd")
			require.ErrorContains(t, err, "list denied")
		})
	}
}

func Test_formatList(t *testing.T) {
	created := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	kubetest.FreezeNow(t, &kube.Now, created.Add(34*24*time.Hour))
	out := ServiceListOutput{Services: []ServiceListItem{
		{Namespace: "argocd", Name: "server", Type: "LoadBalancer", ClusterIP: "10.0.0.1", ExternalIP: "192.168.1.10", Ports: "80:30080/TCP", Health: healthHealthy, Ready: 2, Endpoints: 2, Created: created},
		{Namespace: "argocd", Name: "db", Type: "ExternalName", ExternalIP: "db.example.com", Health: healthExternal, Created: created},
	}}

	text := formatList(out)
	require.Regexp(t, `NAMESPACE\s+NAME\s+TYPE\s+CLUSTER-IP\s+EXTERNAL-IP\s+PORTS\s+HEALTH\s+ENDPOINTS\s+AGE`, text)
	require.Regexp(t, `argocd\s+server\s+LoadBalancer\s+10\.0\.0\.1\s+192\.168\.1\.10\s+80:30080/TCP\s+Healthy\s+2/2\s+34d`, text)
	require.Regexp(t, `argocd\s+db\s+ExternalName\s+n/a\s+db\.example\.com\s+n/a\s+External\s+0/0\s+34d`, text)
	require.Equal(t, "no services found", formatList(ServiceListOutput{}))
	require.Equal(t, `no services in namespace "x"`, formatList(ServiceListOutput{Namespace: "x"}))
}

func connect(t *testing.T, c kube.Clients) *mcp.ClientSession {
	t.Helper()
	return kubetest.Connect(t, func(s *mcp.Server) { Register(s, c) })
}

func testClients() kube.Clients {
	return kube.Clients{Core: fake.NewClientset(kubetest.Namespace("argocd"), service("argocd", "server"),
		slice("argocd", "server", "server-a", ep("server-1", true, false)))}
}

func Test_tools(t *testing.T) {
	session := connect(t, testClients())
	svc := map[string]any{"namespace": "argocd", "name": "server"}
	ghost := map[string]any{"namespace": "argocd", "name": "ghost"}
	tests := map[string]struct {
		tool string
		args map[string]any
		text []string
	}{
		"list":     {tool: "service_list", args: map[string]any{"namespace": "argocd"}, text: []string{"server", "Healthy", "1/1"}},
		"pods":     {tool: "service_pods", args: map[string]any{"services": []any{svc, ghost}}, text: []string{"server-1", `service "argocd/ghost" not found`}},
		"describe": {tool: "service_describe", args: map[string]any{"services": []any{svc}}, text: []string{"# argocd/server Healthy\napiVersion: v1\nkind: Service"}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args})
			require.NoError(t, err)
			require.False(t, result.IsError, result.Content[0].(*mcp.TextContent).Text)
			for _, want := range tc.text {
				require.Contains(t, result.Content[0].(*mcp.TextContent).Text, want)
			}
			require.NotEmpty(t, kubetest.StructuredList(t, result, "services"))
		})
	}
}

func Test_tools_bad_arguments(t *testing.T) {
	refs := make([]any, kube.MaxDescribeTargets+1)
	for i := range refs {
		refs[i] = map[string]any{"namespace": "argocd", "name": fmt.Sprintf("s%d", i)}
	}
	tests := map[string]struct {
		tool   string
		args   map[string]any
		errMsg string
	}{
		"pods empty":        {tool: "service_pods", args: map[string]any{"services": []any{}}, errMsg: "services must contain at least one target"},
		"describe over cap": {tool: "service_describe", args: map[string]any{"services": refs}, errMsg: "at most 5 are allowed"},
		"list missing ns":   {tool: "service_list", args: map[string]any{"namespace": "nope"}, errMsg: `namespace "nope" does not exist`},
	}
	session := connect(t, testClients())

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args})
			require.NoError(t, err)
			require.True(t, result.IsError)
			require.Contains(t, result.Content[0].(*mcp.TextContent).Text, tc.errMsg)
		})
	}
}

func Test_serviceSlices_error(t *testing.T) {
	client := fake.NewClientset()
	client.PrependReactor("list", "endpointslices", kubetest.Forbidden("list", "endpointslices"))
	_, err := serviceSlices(context.Background(), client, kube.ObjectRef{Namespace: "a", Name: "b"})
	require.ErrorContains(t, err, "list denied")
}
