// Package job serves the read-only job tools: status, owned pods, logs, and cleaned definition.
package job

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8s "k8s.io/client-go/kubernetes"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

// Health words; see docs/mcp-k8s.md for the rules.
const (
	healthFailed    = "Failed"
	healthComplete  = "Complete"
	healthSuspended = "Suspended"
	healthRunning   = "Running"
	healthPending   = "Pending"
)

// JobListItem is one job's roster entry; Owner is its controller as Kind/name, usually a cronjob.
type JobListItem struct {
	Namespace   string     `json:"namespace"`
	Name        string     `json:"name"`
	Health      string     `json:"health"`
	Succeeded   int32      `json:"succeeded"`
	Completions int32      `json:"completions"`
	Failed      int32      `json:"failed"`
	Active      int32      `json:"active"`
	Started     *time.Time `json:"started,omitempty"`
	Finished    *time.Time `json:"finished,omitempty"`
	Owner       string     `json:"owner,omitempty"`
	Created     time.Time  `json:"created"`
}

// JobListOutput is the structured result of job_list; Namespace is empty when listing all.
type JobListOutput struct {
	Namespace string        `json:"namespace,omitempty"`
	Jobs      []JobListItem `json:"jobs"`
}

type listInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"the namespace to list; omit to list every namespace"`
}

type targetInput struct {
	Jobs []kube.ObjectRef `json:"jobs" jsonschema:"the jobs to report on, all in one call"`
}

// Register adds the job tools to server.
func Register(server *mcp.Server, c kube.Clients) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "job_list",
		Description: "List Kubernetes jobs with their status (Complete, Failed, Running, Pending, Suspended), succeeded over required completions, failed pods, duration, and owning cronjob. Pass namespace to list one namespace; omit it to list every namespace.",
		Annotations: kube.ReadOnly(),
	}, listHandler(c))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "job_pods",
		Description: "List the pods of one or more Kubernetes jobs in one call, one per attempt, with ready containers, status, restarts, node, and age. Pass every job of interest in jobs as {namespace, name}; a job that does not exist has found false.",
		Annotations: kube.ReadOnly(),
	}, podsHandler(c))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "job_log",
		Description: "Show the recent log output of up to 5 Kubernetes jobs in one call, from each job's 3 newest pods (one per attempt), every main container unless container is given. tail_lines sets how many lines (default 50, at most 500); previous true shows a restarted container's prior instance. Pass jobs as {namespace, name}.",
		Annotations: kube.ReadOnly(),
	}, logHandler(c))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "job_describe",
		Description: "Describe up to 5 Kubernetes jobs in one call: status plus the full job object as YAML (spec, pod template, backoff and deadline settings, conditions, status), with literal environment values redacted. Pass jobs as {namespace, name}; a job that does not exist has found false.",
		Annotations: kube.ReadOnly(),
	}, describeHandler(c))
}

func listHandler(c kube.Clients) mcp.ToolHandlerFor[listInput, JobListOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in listInput) (*mcp.CallToolResult, JobListOutput, error) {
		out, err := listJobs(ctx, c.Core, in.Namespace)
		if err != nil {
			return nil, JobListOutput{}, err
		}
		return kube.TextResult(formatList(out)), out, nil
	}
}

func podsHandler(c kube.Clients) mcp.ToolHandlerFor[targetInput, JobPodsOutput] {
	return kube.PerTarget(
		func(in targetInput) ([]kube.ObjectRef, error) {
			return kube.ObjectTargets("jobs", in.Jobs, kube.MaxTargets)
		},
		func(ctx context.Context, ref kube.ObjectRef) (kube.WorkloadPods, error) {
			return getPods(ctx, c.Core, ref)
		},
		kube.FormatWorkloadPods("job"),
		func(j []kube.WorkloadPods) JobPodsOutput { return JobPodsOutput{Jobs: j} },
	)
}

func describeHandler(c kube.Clients) mcp.ToolHandlerFor[targetInput, JobDescribeOutput] {
	return kube.PerTarget(
		func(in targetInput) ([]kube.ObjectRef, error) {
			return kube.ObjectTargets("jobs", in.Jobs, kube.MaxDescribeTargets)
		},
		func(ctx context.Context, ref kube.ObjectRef) (kube.Described, error) {
			return describe(ctx, c.Core, ref)
		},
		kube.FormatDescribed("job"),
		func(j []kube.Described) JobDescribeOutput { return JobDescribeOutput{Jobs: j} },
	)
}

func listJobs(ctx context.Context, client k8s.Interface, namespace string) (JobListOutput, error) {
	namespace = strings.TrimSpace(namespace)
	list, err := client.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return JobListOutput{}, err
	}
	if err := kube.CheckNamespace(ctx, client, namespace, len(list.Items)); err != nil {
		return JobListOutput{}, err
	}
	out := JobListOutput{Namespace: namespace, Jobs: make([]JobListItem, 0, len(list.Items))}
	for i := range list.Items {
		j := &list.Items[i]
		item := JobListItem{
			Namespace: j.Namespace, Name: j.Name, Health: health(j), Succeeded: j.Status.Succeeded,
			Completions: completions(j), Failed: j.Status.Failed, Active: j.Status.Active, Created: j.CreationTimestamp.Time,
		}
		if j.Status.StartTime != nil {
			item.Started = &j.Status.StartTime.Time
		}
		if j.Status.CompletionTime != nil {
			item.Finished = &j.Status.CompletionTime.Time
		}
		if ref := metav1.GetControllerOf(j); ref != nil {
			item.Owner = ref.Kind + "/" + ref.Name
		}
		out.Jobs = append(out.Jobs, item)
	}
	return out, nil
}

// getJob returns nil and a note on a miss; the note names the namespace when that is what is missing.
func getJob(ctx context.Context, client k8s.Interface, ref kube.ObjectRef) (*batchv1.Job, string, error) {
	j, found, note, err := kube.GetNamespaced(ctx, client, ref, client.BatchV1().Jobs(ref.Namespace).Get)
	if !found {
		return nil, note, err
	}
	return j, "", nil
}

// health reads the job's terminal conditions first, since a finished job keeps its last counts; FailureTarget
// marks a job already failing while its pods terminate.
func health(j *batchv1.Job) string {
	switch {
	case hasCondition(j, batchv1.JobFailed), hasCondition(j, batchv1.JobFailureTarget):
		return healthFailed
	case hasCondition(j, batchv1.JobComplete):
		return healthComplete
	case j.Spec.Suspend != nil && *j.Spec.Suspend:
		return healthSuspended
	case j.Status.Active > 0:
		return healthRunning
	default:
		return healthPending
	}
}

func hasCondition(j *batchv1.Job, t batchv1.JobConditionType) bool {
	for _, c := range j.Status.Conditions {
		if c.Type == t && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// completions applies the API default of 1 when spec.completions is unset.
func completions(j *batchv1.Job) int32 {
	if j.Spec.Completions == nil {
		return 1
	}
	return *j.Spec.Completions
}

func formatList(out JobListOutput) string {
	if len(out.Jobs) == 0 {
		if out.Namespace != "" {
			return fmt.Sprintf("no jobs in namespace %q", out.Namespace)
		}
		return "no jobs found"
	}
	rows := make([][]string, 0, len(out.Jobs))
	for _, j := range out.Jobs {
		rows = append(rows, []string{
			j.Namespace, j.Name, j.Health, fmt.Sprintf("%d/%d", j.Succeeded, j.Completions), strconv.Itoa(int(j.Failed)),
			kube.OrNone(duration(j.Started, j.Finished)), kube.OrNone(j.Owner), kube.Age(j.Created),
		})
	}
	return kube.Table([]string{"NAMESPACE", "NAME", "HEALTH", "COMPLETIONS", "FAILED", "DURATION", "OWNER", "AGE"}, rows)
}

// duration runs from start to finish, or to now while the job is still running.
func duration(started, finished *time.Time) string {
	if started == nil {
		return ""
	}
	end := kube.Now()
	if finished != nil {
		end = *finished
	}
	return kube.Duration(end.Sub(*started))
}
