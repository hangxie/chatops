package kube

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

func Test_OwnedPods(t *testing.T) {
	mine := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-abc", UID: "mine"}}
	other := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "other-abc", UID: "other"}}
	client := fake.NewClientset(
		kubetest.WorkloadPod("ns", "web-1", "web", kubetest.OwnedBy(mine)),
		kubetest.WorkloadPod("ns", "web-stray", "web"),
		kubetest.WorkloadPod("ns", "other-1", "web", kubetest.OwnedBy(other)),
		kubetest.WorkloadPod("ns", "api-1", "api", kubetest.OwnedBy(mine)),
		kubetest.WorkloadPod("elsewhere", "web-2", "web", kubetest.OwnedBy(mine)),
	)
	selector := labels.SelectorFromSet(labels.Set{"app": "web"})
	owners := map[types.UID]bool{"mine": true}

	out, err := OwnedPods(context.Background(), client, "ns", selector, owners, func(p *corev1.Pod, owner types.UID) string {
		return string(owner)
	})
	require.NoError(t, err)
	require.True(t, out.Found)
	require.Equal(t, "app=web", out.Selector)
	require.Len(t, out.Pods, 1)
	require.Equal(t, "web-1", out.Pods[0].Name)
	require.Equal(t, "mine", out.Pods[0].Revision)

	out, err = OwnedPods(context.Background(), client, "ns", selector, owners, nil)
	require.NoError(t, err)
	require.Empty(t, out.Pods[0].Revision)

	client.PrependReactor("list", "pods", kubetest.Forbidden("list", "pods"))
	_, err = OwnedPods(context.Background(), client, "ns", selector, owners, nil)
	require.ErrorContains(t, err, "list denied")
}

func Test_Revision(t *testing.T) {
	tests := map[string]struct {
		pod, current, want string
	}{
		"current":    {pod: "b", current: "b", want: RevisionCurrent},
		"old":        {pod: "a", current: "b", want: RevisionOld},
		"no current": {pod: "a", current: "", want: RevisionUnknown},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, Revision(tc.pod, tc.current))
		})
	}
}

func Test_FormatWorkloadPods(t *testing.T) {
	created := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	kubetest.FreezeNow(t, &Now, created.Add(time.Hour))
	format := FormatWorkloadPods("statefulset")
	tests := map[string]struct {
		w    WorkloadPods
		want []string
	}{
		"pods": {
			w: WorkloadPods{
				Namespace: "db", Name: "pg", Found: true, Health: "Healthy", Selector: "app=pg",
				Pods: []PodSummary{{Name: "pg-0", Ready: 1, Containers: 1, Status: "Running", Node: "worker-0", Created: created, Revision: RevisionCurrent}},
			},
			want: []string{"db/pg Healthy (selector app=pg)\n", "pg-0", "current"},
		},
		"no pods":           {w: WorkloadPods{Namespace: "db", Name: "idle", Found: true, Health: "ScaledToZero", Selector: "app=idle"}, want: []string{"db/idle ScaledToZero (selector app=idle)\nno pods owned by this statefulset"}},
		"missing":           {w: WorkloadPods{Namespace: "db", Name: "ghost"}, want: []string{`statefulset "db/ghost" not found`}},
		"missing namespace": {w: WorkloadPods{Namespace: "nope", Name: "x", Note: "gone"}, want: []string{`statefulset "nope/x" not found: gone`}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			text := format(tc.w)
			for _, w := range tc.want {
				require.Contains(t, text, w)
			}
		})
	}
}

func Test_FormatWorkloadList(t *testing.T) {
	created := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	kubetest.FreezeNow(t, &Now, created.Add(34*24*time.Hour))
	items := []WorkloadListItem{
		{Namespace: "db", Name: "pg", Health: "Healthy", Desired: 3, Ready: 3, Updated: 3, Available: 3, Created: created},
		{Namespace: "db", Name: "redis", Health: "Progressing", Desired: 2, Ready: 1, Updated: 1, Available: 1, Created: created},
	}

	text := FormatWorkloadList("statefulsets", "", items)
	require.Regexp(t, `NAMESPACE\s+NAME\s+HEALTH\s+READY\s+UP-TO-DATE\s+AVAILABLE\s+AGE`, text)
	require.Regexp(t, `db\s+pg\s+Healthy\s+3/3\s+3\s+3\s+34d`, text)
	require.Regexp(t, `db\s+redis\s+Progressing\s+1/2\s+1\s+1\s+34d`, text)
	require.Equal(t, "no statefulsets found", FormatWorkloadList("statefulsets", "", nil))
	require.Equal(t, `no statefulsets in namespace "db"`, FormatWorkloadList("statefulsets", "db", nil))
}

func Test_CheckNamespace(t *testing.T) {
	client := fake.NewClientset(kubetest.Namespace("db"))
	require.NoError(t, CheckNamespace(context.Background(), client, "db", 0))
	require.NoError(t, CheckNamespace(context.Background(), client, "nope", 3))
	require.NoError(t, CheckNamespace(context.Background(), client, "", 0))
	require.EqualError(t, CheckNamespace(context.Background(), client, "nope", 0), `namespace "nope" does not exist`)

	client.PrependReactor("get", "namespaces", kubetest.Forbidden("get", "namespaces"))
	require.ErrorContains(t, CheckNamespace(context.Background(), client, "db", 0), "get denied")
}
