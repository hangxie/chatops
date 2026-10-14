package replicaset

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

type opt func(*appsv1.ReplicaSet)

// replicaSet builds a healthy replicaset of 2 selecting app=<name>.
func replicaSet(namespace, name string, opts ...opt) *appsv1.ReplicaSet {
	r := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: types.UID(namespace + "-" + name), Generation: 1},
		Spec: appsv1.ReplicaSetSpec{
			Replicas: kubetest.Ptr(int32(2)),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
		},
		Status: appsv1.ReplicaSetStatus{ObservedGeneration: 1, Replicas: 2, ReadyReplicas: 2, AvailableReplicas: 2},
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

func withStatus(replicas, available int32) opt {
	return func(r *appsv1.ReplicaSet) {
		r.Status.Replicas, r.Status.AvailableReplicas, r.Status.ReadyReplicas = replicas, available, available
	}
}

func ownedByDeployment(name string) opt {
	return func(r *appsv1.ReplicaSet) {
		r.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: name, UID: "d", Controller: kubetest.Ptr(true)}}
	}
}

func Test_health(t *testing.T) {
	zero := func(r *appsv1.ReplicaSet) { r.Spec.Replicas = kubetest.Ptr(int32(0)) }
	tests := map[string]struct {
		r    *appsv1.ReplicaSet
		want string
	}{
		"healthy":                 {r: replicaSet("ns", "web"), want: healthHealthy},
		"scaled to zero":          {r: replicaSet("ns", "web", zero, withStatus(0, 0)), want: healthScaledToZero},
		"scaling down to zero":    {r: replicaSet("ns", "web", zero, withStatus(1, 1)), want: healthProgressing},
		"generation not observed": {r: replicaSet("ns", "web", func(r *appsv1.ReplicaSet) { r.Generation = 2 }), want: healthProgressing},
		"scaling up":              {r: replicaSet("ns", "web", withStatus(1, 1)), want: healthProgressing},
		"degraded":                {r: replicaSet("ns", "web", withStatus(2, 1)), want: healthDegraded},
		"unset replicas":          {r: replicaSet("ns", "web", func(r *appsv1.ReplicaSet) { r.Spec.Replicas = nil }, withStatus(1, 1)), want: healthHealthy},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, health(tc.r))
		})
	}
}

func Test_listReplicaSets(t *testing.T) {
	created := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	client := fake.NewClientset(kubetest.Namespace("argocd"), kubetest.Namespace("empty"),
		replicaSet("argocd", "server-abc", ownedByDeployment("server"), func(r *appsv1.ReplicaSet) { r.CreationTimestamp = metav1.NewTime(created) }),
		replicaSet("argocd", "bare"),
		replicaSet("other", "web"),
	)
	tests := map[string]struct {
		namespace string
		want      []string
		errMsg    string
	}{
		"one namespace":     {namespace: "argocd", want: []string{"argocd/server-abc", "argocd/bare"}},
		"every namespace":   {want: []string{"argocd/server-abc", "argocd/bare", "other/web"}},
		"empty namespace":   {namespace: "empty", want: []string{}},
		"missing namespace": {namespace: "nope", errMsg: `namespace "nope" does not exist`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			out, err := listReplicaSets(context.Background(), client, tc.namespace)
			if tc.errMsg != "" {
				require.EqualError(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			got := []string{}
			for _, r := range out.ReplicaSets {
				got = append(got, r.Namespace+"/"+r.Name)
			}
			require.ElementsMatch(t, tc.want, got)
		})
	}

	out, err := listReplicaSets(context.Background(), client, "argocd")
	require.NoError(t, err)
	require.Contains(t, out.ReplicaSets, ReplicaSetListItem{
		Namespace: "argocd", Name: "server-abc", Health: healthHealthy,
		Desired: 2, Current: 2, Ready: 2, Owner: "Deployment/server", Created: created,
	})

	client.PrependReactor("list", "replicasets", kubetest.Forbidden("list", "replicasets"))
	_, err = listReplicaSets(context.Background(), client, "argocd")
	require.ErrorContains(t, err, "list denied")
}

func Test_formatList(t *testing.T) {
	created := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	kubetest.FreezeNow(t, &kube.Now, created.Add(34*24*time.Hour))
	out := ReplicaSetListOutput{ReplicaSets: []ReplicaSetListItem{
		{Namespace: "argocd", Name: "server-abc", Health: healthHealthy, Desired: 2, Current: 2, Ready: 2, Owner: "Deployment/server", Created: created},
		{Namespace: "argocd", Name: "bare", Health: healthDegraded, Desired: 2, Current: 2, Ready: 1, Created: created},
	}}

	text := formatList(out)
	require.Regexp(t, `NAMESPACE\s+NAME\s+HEALTH\s+DESIRED\s+CURRENT\s+READY\s+OWNER\s+AGE`, text)
	require.Regexp(t, `argocd\s+server-abc\s+Healthy\s+2\s+2\s+2/2\s+Deployment/server\s+34d`, text)
	require.Regexp(t, `argocd\s+bare\s+Degraded\s+2\s+2\s+1/2\s+n/a\s+34d`, text)
	require.Equal(t, `no replicasets in namespace "x"`, formatList(ReplicaSetListOutput{Namespace: "x"}))
}

func connect(t *testing.T, c kube.Clients) *mcp.ClientSession {
	t.Helper()
	return kubetest.Connect(t, func(s *mcp.Server) { Register(s, c) })
}

func testClients() kube.Clients {
	r := replicaSet("argocd", "server-abc")
	return kube.Clients{Core: fake.NewClientset(kubetest.Namespace("argocd"), r,
		kubetest.WorkloadPod("argocd", "server-abc-1", "server-abc", kubetest.OwnedBy(r)))}
}

func Test_tools(t *testing.T) {
	session := connect(t, testClients())
	rs := map[string]any{"namespace": "argocd", "name": "server-abc"}
	ghost := map[string]any{"namespace": "argocd", "name": "ghost"}
	tests := map[string]struct {
		tool string
		args map[string]any
		text []string
	}{
		"list":     {tool: "replicaset_list", args: map[string]any{"namespace": "argocd"}, text: []string{"server-abc", "Healthy"}},
		"pods":     {tool: "replicaset_pods", args: map[string]any{"replicasets": []any{rs, ghost}}, text: []string{"server-abc-1", `replicaset "argocd/ghost" not found`}},
		"describe": {tool: "replicaset_describe", args: map[string]any{"replicasets": []any{rs}}, text: []string{"# argocd/server-abc Healthy\napiVersion: apps/v1\nkind: ReplicaSet"}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args})
			require.NoError(t, err)
			require.False(t, result.IsError, result.Content[0].(*mcp.TextContent).Text)
			for _, want := range tc.text {
				require.Contains(t, result.Content[0].(*mcp.TextContent).Text, want)
			}
			require.NotEmpty(t, kubetest.StructuredList(t, result, "replicasets"))
		})
	}
}

func Test_tools_bad_arguments(t *testing.T) {
	refs := make([]any, kube.MaxDescribeTargets+1)
	for i := range refs {
		refs[i] = map[string]any{"namespace": "argocd", "name": fmt.Sprintf("r%d", i)}
	}
	tests := map[string]struct {
		tool   string
		args   map[string]any
		errMsg string
	}{
		"pods empty":        {tool: "replicaset_pods", args: map[string]any{"replicasets": []any{}}, errMsg: "replicasets must contain at least one target"},
		"describe over cap": {tool: "replicaset_describe", args: map[string]any{"replicasets": refs}, errMsg: "at most 5 are allowed"},
		"list missing ns":   {tool: "replicaset_list", args: map[string]any{"namespace": "nope"}, errMsg: `namespace "nope" does not exist`},
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
