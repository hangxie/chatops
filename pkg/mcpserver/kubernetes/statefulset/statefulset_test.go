package statefulset

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

type opt func(*appsv1.StatefulSet)

// statefulSet builds a healthy, fully rolled-out statefulset selecting app=<name> at revision <name>-1.
func statefulSet(namespace, name string, opts ...opt) *appsv1.StatefulSet {
	s := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: types.UID(namespace + "-" + name), Generation: 1},
		Spec: appsv1.StatefulSetSpec{
			Replicas: kubetest.Ptr(int32(3)),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
		},
		Status: appsv1.StatefulSetStatus{
			ObservedGeneration: 1, Replicas: 3, ReadyReplicas: 3, UpdatedReplicas: 3, AvailableReplicas: 3,
			CurrentRevision: name + "-1", UpdateRevision: name + "-1",
		},
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

func withStatus(replicas, updated, available int32, current, update string) opt {
	return func(s *appsv1.StatefulSet) {
		s.Status.Replicas, s.Status.UpdatedReplicas, s.Status.AvailableReplicas = replicas, updated, available
		s.Status.ReadyReplicas = available
		s.Status.CurrentRevision, s.Status.UpdateRevision = current, update
	}
}

func partition(p int32) opt {
	return func(s *appsv1.StatefulSet) {
		s.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{
			Type:          appsv1.RollingUpdateStatefulSetStrategyType,
			RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: kubetest.Ptr(p)},
		}
	}
}

func Test_health(t *testing.T) {
	tests := map[string]struct {
		s    *appsv1.StatefulSet
		want string
	}{
		"healthy":                 {s: statefulSet("db", "pg"), want: healthHealthy},
		"scaled to zero":          {s: statefulSet("db", "pg", func(s *appsv1.StatefulSet) { s.Spec.Replicas = kubetest.Ptr(int32(0)) }, withStatus(0, 0, 0, "a", "a")), want: healthScaledToZero},
		"generation not observed": {s: statefulSet("db", "pg", func(s *appsv1.StatefulSet) { s.Generation = 2 }), want: healthProgressing},
		"rolling update":          {s: statefulSet("db", "pg", withStatus(3, 1, 3, "pg-1", "pg-2")), want: healthProgressing},
		"revisions not swapped":   {s: statefulSet("db", "pg", withStatus(3, 3, 3, "pg-1", "pg-2")), want: healthProgressing},
		"partition in progress":   {s: statefulSet("db", "pg", partition(2), withStatus(3, 0, 3, "pg-1", "pg-2")), want: healthProgressing},
		"partition complete":      {s: statefulSet("db", "pg", partition(2), withStatus(3, 1, 3, "pg-1", "pg-2")), want: healthHealthy},
		"on delete pending":       {s: statefulSet("db", "pg", func(s *appsv1.StatefulSet) { s.Spec.UpdateStrategy.Type = appsv1.OnDeleteStatefulSetStrategyType }, withStatus(3, 0, 3, "pg-1", "pg-2")), want: healthHealthy},
		"scale-down":              {s: statefulSet("db", "pg", withStatus(4, 4, 4, "pg-1", "pg-1")), want: healthProgressing},
		"degraded":                {s: statefulSet("db", "pg", withStatus(3, 3, 2, "pg-1", "pg-1")), want: healthDegraded},
		"unset replicas":          {s: statefulSet("db", "pg", func(s *appsv1.StatefulSet) { s.Spec.Replicas = nil }, withStatus(1, 1, 1, "a", "a")), want: healthHealthy},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, health(tc.s))
		})
	}
}

func Test_listStatefulSets(t *testing.T) {
	created := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	client := fake.NewClientset(kubetest.Namespace("db"), kubetest.Namespace("empty"),
		statefulSet("db", "pg", func(s *appsv1.StatefulSet) { s.CreationTimestamp = metav1.NewTime(created) }),
		statefulSet("db", "redis", withStatus(3, 3, 2, "r-1", "r-1")),
		statefulSet("other", "kafka"),
	)
	tests := map[string]struct {
		namespace string
		want      []string
		errMsg    string
	}{
		"one namespace":     {namespace: " db ", want: []string{"db/pg", "db/redis"}},
		"every namespace":   {want: []string{"db/pg", "db/redis", "other/kafka"}},
		"empty namespace":   {namespace: "empty", want: []string{}},
		"missing namespace": {namespace: "nope", errMsg: `namespace "nope" does not exist`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			out, err := listStatefulSets(context.Background(), client, tc.namespace)
			if tc.errMsg != "" {
				require.EqualError(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			got := []string{}
			for _, s := range out.StatefulSets {
				got = append(got, s.Namespace+"/"+s.Name)
			}
			require.ElementsMatch(t, tc.want, got)
		})
	}

	out, err := listStatefulSets(context.Background(), client, "db")
	require.NoError(t, err)
	require.Contains(t, out.StatefulSets, kube.WorkloadListItem{
		Namespace: "db", Name: "pg", Health: healthHealthy,
		Desired: 3, Ready: 3, Updated: 3, Available: 3, Created: created,
	})

	client.PrependReactor("list", "statefulsets", kubetest.Forbidden("list", "statefulsets"))
	_, err = listStatefulSets(context.Background(), client, "db")
	require.ErrorContains(t, err, "list denied")
}

func connect(t *testing.T, c kube.Clients) *mcp.ClientSession {
	t.Helper()
	return kubetest.Connect(t, func(s *mcp.Server) { Register(s, c) })
}

func testClients() kube.Clients {
	s := statefulSet("db", "pg")
	return kube.Clients{Core: fake.NewClientset(kubetest.Namespace("db"), s,
		kubetest.WorkloadPod("db", "pg-0", "pg", ownedBy(s), revision("pg-1")))}
}

func Test_tools(t *testing.T) {
	session := connect(t, testClients())
	pg := map[string]any{"namespace": "db", "name": "pg"}
	ghost := map[string]any{"namespace": "db", "name": "ghost"}
	tests := map[string]struct {
		tool string
		args map[string]any
		text []string
	}{
		"list":     {tool: "statefulset_list", args: map[string]any{"namespace": "db"}, text: []string{"pg", "Healthy"}},
		"pods":     {tool: "statefulset_pods", args: map[string]any{"statefulsets": []any{pg, ghost}}, text: []string{"pg-0", "current", `statefulset "db/ghost" not found`}},
		"describe": {tool: "statefulset_describe", args: map[string]any{"statefulsets": []any{pg}}, text: []string{"# db/pg Healthy\napiVersion: apps/v1\nkind: StatefulSet"}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args})
			require.NoError(t, err)
			require.False(t, result.IsError, result.Content[0].(*mcp.TextContent).Text)
			for _, want := range tc.text {
				require.Contains(t, result.Content[0].(*mcp.TextContent).Text, want)
			}
			require.NotEmpty(t, kubetest.StructuredList(t, result, "statefulsets"))
		})
	}
}

func Test_tools_bad_arguments(t *testing.T) {
	refs := make([]any, kube.MaxDescribeTargets+1)
	for i := range refs {
		refs[i] = map[string]any{"namespace": "db", "name": fmt.Sprintf("s%d", i)}
	}
	tests := map[string]struct {
		tool   string
		args   map[string]any
		errMsg string
	}{
		"pods empty":        {tool: "statefulset_pods", args: map[string]any{"statefulsets": []any{}}, errMsg: "statefulsets must contain at least one target"},
		"describe over cap": {tool: "statefulset_describe", args: map[string]any{"statefulsets": refs}, errMsg: "at most 5 are allowed"},
		"list missing ns":   {tool: "statefulset_list", args: map[string]any{"namespace": "nope"}, errMsg: `namespace "nope" does not exist`},
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

func ownedBy(s *appsv1.StatefulSet) kubetest.PodOpt {
	return func(p *corev1.Pod) {
		p.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(s, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))}
	}
}

func revision(hash string) kubetest.PodOpt {
	return func(p *corev1.Pod) { p.Labels[appsv1.ControllerRevisionHashLabelKey] = hash }
}

func Test_tools_capped(t *testing.T) {
	s := statefulSet("db", "pg")
	session := connect(t, kube.Clients{MaxResults: 1, Core: fake.NewClientset(kubetest.Namespace("db"), s, statefulSet("db", "redis"),
		kubetest.WorkloadPod("db", "pg-0", "pg", ownedBy(s), revision("pg-1")),
		kubetest.WorkloadPod("db", "pg-1", "pg", ownedBy(s), revision("pg-1")))})

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "statefulset_list"})
	require.NoError(t, err)
	kubetest.RequireCapped(t, result, "statefulsets", 1, 2)

	result, err = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "statefulset_pods", Arguments: map[string]any{"statefulsets": []any{map[string]any{"namespace": "db", "name": "pg"}}},
	})
	require.NoError(t, err)
	kubetest.RequireCappedTarget(t, result, "statefulsets", "pods", 1, 2)
}
