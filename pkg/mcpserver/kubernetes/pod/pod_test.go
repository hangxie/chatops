package pod

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

func connect(t *testing.T, c kube.Clients) *mcp.ClientSession {
	t.Helper()
	return kubetest.Connect(t, func(s *mcp.Server) { Register(s, c) })
}

func testClients() kube.Clients {
	return kube.Clients{
		Core:    fake.NewClientset(kubetest.Namespace("argocd"), kubetest.WorkloadPod("argocd", "server-1", "server")),
		Metrics: newMetricsFake(),
	}
}

func Test_listPods(t *testing.T) {
	created := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "server-abc", UID: "rs-uid"}}
	client := fake.NewClientset(kubetest.Namespace("argocd"), kubetest.Namespace("empty"),
		kubetest.WorkloadPod("argocd", "server-1", "server", kubetest.OwnedBy(rs), func(p *corev1.Pod) { p.CreationTimestamp = metav1.NewTime(created) }),
		kubetest.WorkloadPod("argocd", "bare", "bare"),
		kubetest.WorkloadPod("other", "web-1", "web"),
	)
	tests := map[string]struct {
		namespace string
		want      []string
		errMsg    string
	}{
		"one namespace":     {namespace: "argocd", want: []string{"argocd/server-1", "argocd/bare"}},
		"every namespace":   {want: []string{"argocd/server-1", "argocd/bare", "other/web-1"}},
		"empty namespace":   {namespace: " empty ", want: []string{}},
		"missing namespace": {namespace: "nope", errMsg: `namespace "nope" does not exist`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			out, err := listPods(context.Background(), client, tc.namespace, "")
			if tc.errMsg != "" {
				require.EqualError(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			got := []string{}
			for _, p := range out.Pods {
				got = append(got, p.Namespace+"/"+p.Name)
			}
			require.ElementsMatch(t, tc.want, got)
		})
	}

	out, err := listPods(context.Background(), client, "argocd", "")
	require.NoError(t, err)
	require.Contains(t, out.Pods, PodListItem{
		Namespace: "argocd", Name: "server-1", Ready: 1, Containers: 1, Status: "Running",
		Node: "worker-0", Owner: "ReplicaSet/server-abc", Created: created,
	})
}

func Test_listPods_node_filter(t *testing.T) {
	client := fake.NewClientset()
	var selector string
	client.PrependReactor("list", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		selector = a.(k8stesting.ListAction).GetListRestrictions().Fields.String()
		return true, &corev1.PodList{}, nil
	})

	out, err := listPods(context.Background(), client, "", " worker-4 ")
	require.NoError(t, err)
	require.Equal(t, "spec.nodeName=worker-4", selector)
	require.Equal(t, "worker-4", out.Node)
}

func Test_listPods_errors(t *testing.T) {
	tests := map[string]struct {
		verb, resource string
	}{
		"list pods":     {verb: "list", resource: "pods"},
		"get namespace": {verb: "get", resource: "namespaces"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			client := fake.NewClientset()
			client.PrependReactor(tc.verb, tc.resource, kubetest.Forbidden(tc.verb, tc.resource))
			_, err := listPods(context.Background(), client, "argocd", "")
			require.ErrorContains(t, err, tc.verb+" denied")
		})
	}
}

func Test_getPod(t *testing.T) {
	client := fake.NewClientset(kubetest.Namespace("argocd"), kubetest.WorkloadPod("argocd", "server-1", "server"))
	tests := map[string]struct {
		ref   kube.ObjectRef
		found bool
		note  string
	}{
		"found":             {ref: kube.ObjectRef{Namespace: "argocd", Name: "server-1"}, found: true},
		"missing":           {ref: kube.ObjectRef{Namespace: "argocd", Name: "ghost"}},
		"missing namespace": {ref: kube.ObjectRef{Namespace: "nope", Name: "x"}, note: `namespace "nope" does not exist`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			p, note, err := getPod(context.Background(), client, tc.ref)
			require.NoError(t, err)
			require.Equal(t, tc.found, p != nil)
			require.Equal(t, tc.note, note)
		})
	}
}

func Test_pod_tools(t *testing.T) {
	session := connect(t, testClients())
	ref := map[string]any{"namespace": "argocd", "name": "server-1"}
	ghost := map[string]any{"namespace": "argocd", "name": "ghost"}
	tests := map[string]struct {
		tool  string
		args  map[string]any
		field string
		text  []string
	}{
		"list":      {tool: "pod_list", args: map[string]any{"namespace": "argocd"}, field: "pods", text: []string{"server-1"}},
		"describe":  {tool: "pod_describe", args: map[string]any{"pods": []any{ref, ghost}}, field: "pods", text: []string{"# argocd/server-1 Running\napiVersion: v1", `pod "argocd/ghost" not found`}},
		"log":       {tool: "pod_log", args: map[string]any{"pods": []any{ref}}, field: "pods", text: []string{"== argocd/server-1 [main] ==\nfake logs"}},
		"resources": {tool: "pod_resources", args: map[string]any{"pods": []any{ref}}, field: "pods", text: []string{"CPU REQUEST", "note: cpu and memory usage unavailable"}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args})
			require.NoError(t, err)
			require.False(t, result.IsError, result.Content[0].(*mcp.TextContent).Text)
			for _, want := range tc.text {
				require.Contains(t, result.Content[0].(*mcp.TextContent).Text, want)
			}
			require.NotEmpty(t, kubetest.StructuredList(t, result, tc.field))
		})
	}
}

func Test_pod_tools_bad_arguments(t *testing.T) {
	refs := func(n int) []any {
		out := make([]any, n)
		for i := range out {
			out[i] = map[string]any{"namespace": "argocd", "name": "p" + string(rune('a'+i))}
		}
		return out
	}
	tests := map[string]struct {
		tool   string
		args   map[string]any
		errMsg string
	}{
		"describe over cap":  {tool: "pod_describe", args: map[string]any{"pods": refs(kube.MaxDescribeTargets + 1)}, errMsg: "at most 5 are allowed"},
		"log over cap":       {tool: "pod_log", args: map[string]any{"pods": refs(kube.MaxLogTargets + 1)}, errMsg: "at most 5 are allowed"},
		"log tail too large": {tool: "pod_log", args: map[string]any{"pods": refs(1), "tail_lines": 501}, errMsg: "tail_lines must be between 1 and 500"},
		"resources empty":    {tool: "pod_resources", args: map[string]any{"pods": []any{}}, errMsg: "pods must contain at least one target"},
		"list ns missing":    {tool: "pod_list", args: map[string]any{"namespace": "nope"}, errMsg: `namespace "nope" does not exist`},
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

func Test_pod_list_tool_capped(t *testing.T) {
	session := connect(t, kube.Clients{MaxResults: 1, Core: fake.NewClientset(kubetest.Namespace("argocd"),
		kubetest.WorkloadPod("argocd", "server-1", "server"), kubetest.WorkloadPod("argocd", "server-2", "server"))})

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "pod_list"})
	require.NoError(t, err)
	kubetest.RequireCapped(t, result, "pods", 1, 2)
}
