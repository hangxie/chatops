package job

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

func Test_getLog(t *testing.T) {
	base := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	j := job("db", "migrate", condition(batchv1.JobFailed))
	objs := []runtime.Object{kubetest.Namespace("db"), j}
	for i, name := range []string{"try-1", "try-2", "try-3", "try-4"} {
		objs = append(objs, kubetest.WorkloadPod("db", name, "migrate", ownedBy(j), createdAt(base.Add(time.Duration(i)*time.Minute))))
	}
	client := fake.NewClientset(objs...)

	out, err := getLog(context.Background(), client, kube.ObjectRef{Namespace: "db", Name: "migrate"}, kube.LogOptions{TailLines: 5, Previous: true})
	require.NoError(t, err)
	require.True(t, out.Found)
	require.Equal(t, healthFailed, out.Health)
	require.Equal(t, 4, out.Total)
	require.True(t, out.Previous)
	names := []string{}
	for _, p := range out.Pods {
		names = append(names, p.Pod)
		require.Equal(t, []kube.ContainerLog{{Container: "main", Log: "fake logs"}}, p.Containers)
	}
	require.Equal(t, []string{"try-4", "try-3", "try-2"}, names) // the newest attempts, newest first

	out, err = getLog(context.Background(), client, kube.ObjectRef{Namespace: "db", Name: "migrate"}, kube.LogOptions{Container: "x", TailLines: 5})
	require.NoError(t, err)
	require.Equal(t, `container "x" not found (containers: main)`, out.Pods[0].Note)
}

func Test_getLog_misses_and_errors(t *testing.T) {
	out, err := getLog(context.Background(), fake.NewClientset(), kube.ObjectRef{Namespace: "nope", Name: "x"}, kube.LogOptions{TailLines: 5})
	require.NoError(t, err)
	require.Equal(t, JobLog{Namespace: "nope", Name: "x", Note: `namespace "nope" does not exist`}, out)

	tests := map[string]struct {
		verb, resource string
		j              *batchv1.Job
	}{
		"get job":   {verb: "get", resource: "jobs", j: job("ns", "j")},
		"list pods": {verb: "list", resource: "pods", j: job("ns", "j")},
		"read log":  {verb: "get", resource: "pods", j: job("ns", "j")},
		"bad selector": {verb: "none", resource: "none", j: job("ns", "j", func(j *batchv1.Job) {
			j.Spec.Selector.MatchExpressions = []metav1.LabelSelectorRequirement{{Key: "app", Operator: "Bogus"}}
		})},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			client := fake.NewClientset(tc.j, kubetest.WorkloadPod("ns", "j-a", "j", ownedBy(tc.j)))
			client.PrependReactor(tc.verb, tc.resource, kubetest.Forbidden(tc.verb, tc.resource))
			_, err := getLog(context.Background(), client, kube.ObjectRef{Namespace: "ns", Name: "j"}, kube.LogOptions{TailLines: 5})
			require.Error(t, err)
		})
	}
}

func Test_formatLog(t *testing.T) {
	logs := []kube.ContainerLog{{Container: "main", Log: "boom"}}
	tests := map[string]struct {
		out  JobLog
		want string
	}{
		"all pods": {
			out:  JobLog{Namespace: "db", Name: "m", Found: true, Health: healthFailed, Total: 1, Pods: []PodLog{{Pod: "m-a", Containers: logs}}},
			want: "db/m Failed (1 pods)\n\n== db/m-a [main] ==\nboom",
		},
		"newest only": {
			out:  JobLog{Namespace: "db", Name: "m", Found: true, Health: healthFailed, Total: 5, Previous: true, Pods: []PodLog{{Pod: "m-e", Containers: logs}}},
			want: "db/m Failed (5 pods, showing the newest 1)\n\n== db/m-e [main] (previous) ==\nboom",
		},
		"pod note": {out: JobLog{Namespace: "db", Name: "m", Found: true, Health: healthRunning, Total: 1, Pods: []PodLog{{Pod: "m-a", Note: "no such container"}}}, want: "db/m Running (1 pods)\n\npod \"db/m-a\": no such container"},
		"no pods":  {out: JobLog{Namespace: "db", Name: "m", Found: true, Health: healthPending}, want: "db/m Pending (0 pods)\nno pods to read logs from"},
		"missing":  {out: JobLog{Namespace: "db", Name: "ghost"}, want: `job "db/ghost" not found`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, formatLog(tc.out))
		})
	}
}
