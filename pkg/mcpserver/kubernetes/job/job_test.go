package job

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

type opt func(*batchv1.Job)

// job builds a running job selecting app=<name>, as the job controller labels its pods.
func job(namespace, name string, opts ...opt) *batchv1.Job {
	j := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: types.UID(namespace + "-" + name)},
		Spec:       batchv1.JobSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}}},
		Status:     batchv1.JobStatus{Active: 1},
	}
	for _, o := range opts {
		o(j)
	}
	return j
}

func condition(t batchv1.JobConditionType) opt {
	return func(j *batchv1.Job) {
		j.Status.Active = 0
		j.Status.Conditions = append(j.Status.Conditions, batchv1.JobCondition{Type: t, Status: corev1.ConditionTrue})
	}
}

func ownedBy(j *batchv1.Job) kubetest.PodOpt {
	return func(p *corev1.Pod) {
		p.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(j, batchv1.SchemeGroupVersion.WithKind("Job"))}
	}
}

func createdAt(t time.Time) kubetest.PodOpt {
	return func(p *corev1.Pod) { p.CreationTimestamp = metav1.NewTime(t) }
}

func Test_health(t *testing.T) {
	suspended := func(j *batchv1.Job) { j.Spec.Suspend = kubetest.Ptr(true); j.Status.Active = 0 }
	tests := map[string]struct {
		j    *batchv1.Job
		want string
	}{
		"running":        {j: job("ns", "j"), want: healthRunning},
		"pending":        {j: job("ns", "j", func(j *batchv1.Job) { j.Status.Active = 0 }), want: healthPending},
		"complete":       {j: job("ns", "j", condition(batchv1.JobComplete)), want: healthComplete},
		"failed":         {j: job("ns", "j", condition(batchv1.JobFailed)), want: healthFailed},
		"failure target": {j: job("ns", "j", condition(batchv1.JobFailureTarget)), want: healthFailed},
		"suspended":      {j: job("ns", "j", suspended), want: healthSuspended},
		"false condition": {j: job("ns", "j", func(j *batchv1.Job) {
			j.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionFalse}}
		}), want: healthRunning},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, health(tc.j))
		})
	}
}

func Test_listJobs(t *testing.T) {
	created := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	started, finished := metav1.NewTime(created.Add(time.Minute)), metav1.NewTime(created.Add(3*time.Minute))
	backup := job("db", "backup-1", condition(batchv1.JobComplete), func(j *batchv1.Job) {
		j.CreationTimestamp = metav1.NewTime(created)
		j.Spec.Completions = kubetest.Ptr(int32(2))
		j.Status.Succeeded, j.Status.Failed = 2, 1
		j.Status.StartTime, j.Status.CompletionTime = &started, &finished
		j.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "CronJob", Name: "backup", UID: "cj", Controller: kubetest.Ptr(true)}}
	})
	client := fake.NewClientset(kubetest.Namespace("db"), kubetest.Namespace("empty"), backup, job("db", "migrate"), job("other", "seed"))
	tests := map[string]struct {
		namespace string
		want      []string
		errMsg    string
	}{
		"one namespace":     {namespace: "db", want: []string{"db/backup-1", "db/migrate"}},
		"every namespace":   {want: []string{"db/backup-1", "db/migrate", "other/seed"}},
		"empty namespace":   {namespace: "empty", want: []string{}},
		"missing namespace": {namespace: "nope", errMsg: `namespace "nope" does not exist`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			out, err := listJobs(context.Background(), client, tc.namespace)
			if tc.errMsg != "" {
				require.EqualError(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			got := []string{}
			for _, j := range out.Jobs {
				got = append(got, j.Namespace+"/"+j.Name)
			}
			require.ElementsMatch(t, tc.want, got)
		})
	}

	out, err := listJobs(context.Background(), client, "db")
	require.NoError(t, err)
	require.Contains(t, out.Jobs, JobListItem{
		Namespace: "db", Name: "backup-1", Health: healthComplete, Succeeded: 2, Completions: 2,
		Failed: 1, Started: &started.Time, Finished: &finished.Time, Owner: "CronJob/backup", Created: created,
	})

	client.PrependReactor("list", "jobs", kubetest.Forbidden("list", "jobs"))
	_, err = listJobs(context.Background(), client, "db")
	require.ErrorContains(t, err, "list denied")
}

func Test_formatList(t *testing.T) {
	created := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	kubetest.FreezeNow(t, &kube.Now, created.Add(2*time.Hour))
	started, finished := created.Add(time.Minute), created.Add(4*time.Minute)
	running := created.Add(time.Hour)
	out := JobListOutput{Jobs: []JobListItem{
		{Namespace: "db", Name: "backup-1", Health: healthComplete, Succeeded: 1, Completions: 1, Started: &started, Finished: &finished, Owner: "CronJob/backup", Created: created},
		{Namespace: "db", Name: "migrate", Health: healthRunning, Completions: 1, Failed: 2, Started: &running, Created: created},
		{Namespace: "db", Name: "queued", Health: healthPending, Completions: 1, Created: created},
	}}

	text := formatList(out)
	require.Regexp(t, `NAMESPACE\s+NAME\s+HEALTH\s+COMPLETIONS\s+FAILED\s+DURATION\s+OWNER\s+AGE`, text)
	require.Regexp(t, `db\s+backup-1\s+Complete\s+1/1\s+0\s+3m\s+CronJob/backup\s+2h`, text)
	require.Regexp(t, `db\s+migrate\s+Running\s+0/1\s+2\s+1h\s+n/a\s+2h`, text)
	require.Regexp(t, `db\s+queued\s+Pending\s+0/1\s+0\s+n/a\s+n/a\s+2h`, text)
	require.Equal(t, "no jobs found", formatList(JobListOutput{}))
	require.Equal(t, `no jobs in namespace "x"`, formatList(JobListOutput{Namespace: "x"}))
}

func connect(t *testing.T, c kube.Clients) *mcp.ClientSession {
	t.Helper()
	return kubetest.Connect(t, func(s *mcp.Server) { Register(s, c) })
}

func testClients() kube.Clients {
	j := job("db", "migrate")
	return kube.Clients{Core: fake.NewClientset(kubetest.Namespace("db"), j, kubetest.WorkloadPod("db", "migrate-x", "migrate", ownedBy(j)))}
}

func Test_tools(t *testing.T) {
	session := connect(t, testClients())
	migrate := map[string]any{"namespace": "db", "name": "migrate"}
	ghost := map[string]any{"namespace": "db", "name": "ghost"}
	tests := map[string]struct {
		tool string
		args map[string]any
		text []string
	}{
		"list":     {tool: "job_list", args: map[string]any{"namespace": "db"}, text: []string{"migrate", "Running"}},
		"pods":     {tool: "job_pods", args: map[string]any{"jobs": []any{migrate, ghost}}, text: []string{"migrate-x", `job "db/ghost" not found`}},
		"log":      {tool: "job_log", args: map[string]any{"jobs": []any{migrate}}, text: []string{"db/migrate Running (1 pods)", "== db/migrate-x [main] ==\nfake logs"}},
		"describe": {tool: "job_describe", args: map[string]any{"jobs": []any{migrate}}, text: []string{"# db/migrate Running\napiVersion: batch/v1\nkind: Job"}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args})
			require.NoError(t, err)
			require.False(t, result.IsError, result.Content[0].(*mcp.TextContent).Text)
			for _, want := range tc.text {
				require.Contains(t, result.Content[0].(*mcp.TextContent).Text, want)
			}
			require.NotEmpty(t, kubetest.StructuredList(t, result, "jobs"))
		})
	}
}

func Test_tools_bad_arguments(t *testing.T) {
	refs := make([]any, kube.MaxDescribeTargets+1)
	for i := range refs {
		refs[i] = map[string]any{"namespace": "db", "name": fmt.Sprintf("j%d", i)}
	}
	tests := map[string]struct {
		tool   string
		args   map[string]any
		errMsg string
	}{
		"pods empty":        {tool: "job_pods", args: map[string]any{"jobs": []any{}}, errMsg: "jobs must contain at least one target"},
		"describe over cap": {tool: "job_describe", args: map[string]any{"jobs": refs}, errMsg: "at most 5 are allowed"},
		"log over cap":      {tool: "job_log", args: map[string]any{"jobs": refs}, errMsg: "at most 5 are allowed"},
		"log tail":          {tool: "job_log", args: map[string]any{"jobs": refs[:1], "tail_lines": -1}, errMsg: "tail_lines must be between 1 and 500"},
		"list missing ns":   {tool: "job_list", args: map[string]any{"namespace": "nope"}, errMsg: `namespace "nope" does not exist`},
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
