package daemonset

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

type opt func(*appsv1.DaemonSet)

// daemonSet builds a healthy, fully rolled-out daemonset on 3 nodes selecting app=<name>.
func daemonSet(namespace, name string, opts ...opt) *appsv1.DaemonSet {
	d := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: types.UID(namespace + "-" + name), Generation: 1},
		Spec:       appsv1.DaemonSetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}}},
		Status: appsv1.DaemonSetStatus{
			ObservedGeneration: 1, DesiredNumberScheduled: 3, CurrentNumberScheduled: 3, NumberReady: 3,
			UpdatedNumberScheduled: 3, NumberAvailable: 3,
		},
	}
	for _, o := range opts {
		o(d)
	}
	return d
}

func withStatus(desired, updated, available int32) opt {
	return func(d *appsv1.DaemonSet) {
		d.Status.DesiredNumberScheduled, d.Status.UpdatedNumberScheduled = desired, updated
		d.Status.NumberAvailable, d.Status.NumberReady = available, available
	}
}

func Test_health(t *testing.T) {
	onDelete := func(d *appsv1.DaemonSet) { d.Spec.UpdateStrategy.Type = appsv1.OnDeleteDaemonSetStrategyType }
	tests := map[string]struct {
		d    *appsv1.DaemonSet
		want string
	}{
		"healthy":                 {d: daemonSet("kube-system", "cilium"), want: healthHealthy},
		"no nodes":                {d: daemonSet("kube-system", "cilium", withStatus(0, 0, 0)), want: healthNoNodes},
		"generation not observed": {d: daemonSet("kube-system", "cilium", func(d *appsv1.DaemonSet) { d.Generation = 2 }), want: healthProgressing},
		"rolling update":          {d: daemonSet("kube-system", "cilium", withStatus(3, 1, 3)), want: healthProgressing},
		"on delete pending":       {d: daemonSet("kube-system", "cilium", onDelete, withStatus(3, 1, 3)), want: healthHealthy},
		"degraded":                {d: daemonSet("kube-system", "cilium", withStatus(3, 3, 2)), want: healthDegraded},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, health(tc.d))
		})
	}
}

func Test_listDaemonSets(t *testing.T) {
	created := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	client := fake.NewClientset(kubetest.Namespace("kube-system"), kubetest.Namespace("empty"),
		daemonSet("kube-system", "cilium", func(d *appsv1.DaemonSet) { d.CreationTimestamp = metav1.NewTime(created) }),
		daemonSet("kube-system", "proxy", withStatus(3, 3, 2)),
		daemonSet("alloy", "alloy"),
	)
	tests := map[string]struct {
		namespace string
		want      []string
		errMsg    string
	}{
		"one namespace":     {namespace: "kube-system", want: []string{"kube-system/cilium", "kube-system/proxy"}},
		"every namespace":   {want: []string{"kube-system/cilium", "kube-system/proxy", "alloy/alloy"}},
		"empty namespace":   {namespace: "empty", want: []string{}},
		"missing namespace": {namespace: "nope", errMsg: `namespace "nope" does not exist`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			out, err := listDaemonSets(context.Background(), client, tc.namespace)
			if tc.errMsg != "" {
				require.EqualError(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			got := []string{}
			for _, d := range out.DaemonSets {
				got = append(got, d.Namespace+"/"+d.Name)
			}
			require.ElementsMatch(t, tc.want, got)
		})
	}

	out, err := listDaemonSets(context.Background(), client, "kube-system")
	require.NoError(t, err)
	require.Contains(t, out.DaemonSets, kube.WorkloadListItem{
		Namespace: "kube-system", Name: "cilium", Health: healthHealthy,
		Desired: 3, Ready: 3, Updated: 3, Available: 3, Created: created,
	})

	client.PrependReactor("list", "daemonsets", kubetest.Forbidden("list", "daemonsets"))
	_, err = listDaemonSets(context.Background(), client, "kube-system")
	require.ErrorContains(t, err, "list denied")
}

func connect(t *testing.T, c kube.Clients) *mcp.ClientSession {
	t.Helper()
	return kubetest.Connect(t, func(s *mcp.Server) { Register(s, c) })
}

func testClients() kube.Clients {
	d := daemonSet("kube-system", "cilium")
	return kube.Clients{Core: fake.NewClientset(kubetest.Namespace("kube-system"), d, controllerRevision(d, 1, "abc"),
		kubetest.WorkloadPod("kube-system", "cilium-x", "cilium", ownedBy(d), revision("abc")))}
}

func Test_tools(t *testing.T) {
	session := connect(t, testClients())
	cilium := map[string]any{"namespace": "kube-system", "name": "cilium"}
	ghost := map[string]any{"namespace": "kube-system", "name": "ghost"}
	tests := map[string]struct {
		tool string
		args map[string]any
		text []string
	}{
		"list":     {tool: "daemonset_list", args: map[string]any{"namespace": "kube-system"}, text: []string{"cilium", "Healthy"}},
		"pods":     {tool: "daemonset_pods", args: map[string]any{"daemonsets": []any{cilium, ghost}}, text: []string{"cilium-x", "current", `daemonset "kube-system/ghost" not found`}},
		"describe": {tool: "daemonset_describe", args: map[string]any{"daemonsets": []any{cilium}}, text: []string{"# kube-system/cilium Healthy\napiVersion: apps/v1\nkind: DaemonSet"}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args})
			require.NoError(t, err)
			require.False(t, result.IsError, result.Content[0].(*mcp.TextContent).Text)
			for _, want := range tc.text {
				require.Contains(t, result.Content[0].(*mcp.TextContent).Text, want)
			}
			require.NotEmpty(t, kubetest.StructuredList(t, result, "daemonsets"))
		})
	}
}

func Test_tools_bad_arguments(t *testing.T) {
	refs := make([]any, kube.MaxDescribeTargets+1)
	for i := range refs {
		refs[i] = map[string]any{"namespace": "kube-system", "name": fmt.Sprintf("d%d", i)}
	}
	tests := map[string]struct {
		tool   string
		args   map[string]any
		errMsg string
	}{
		"pods empty":        {tool: "daemonset_pods", args: map[string]any{"daemonsets": []any{}}, errMsg: "daemonsets must contain at least one target"},
		"describe over cap": {tool: "daemonset_describe", args: map[string]any{"daemonsets": refs}, errMsg: "at most 5 are allowed"},
		"list missing ns":   {tool: "daemonset_list", args: map[string]any{"namespace": "nope"}, errMsg: `namespace "nope" does not exist`},
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

func ownedBy(d *appsv1.DaemonSet) kubetest.PodOpt {
	return func(p *corev1.Pod) {
		p.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(d, appsv1.SchemeGroupVersion.WithKind("DaemonSet"))}
	}
}

func revision(hash string) kubetest.PodOpt {
	return func(p *corev1.Pod) { p.Labels[appsv1.ControllerRevisionHashLabelKey] = hash }
}

// controllerRevision builds a revision controlled by d, labeled with d's selector and the given hash.
func controllerRevision(d *appsv1.DaemonSet, number int64, hash string) *appsv1.ControllerRevision {
	labels := map[string]string{appsv1.ControllerRevisionHashLabelKey: hash}
	for k, v := range d.Spec.Selector.MatchLabels {
		labels[k] = v
	}
	return &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: d.Namespace, Name: d.Name + "-" + hash, Labels: labels,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(d, appsv1.SchemeGroupVersion.WithKind("DaemonSet"))},
		},
		Revision: number,
	}
}
