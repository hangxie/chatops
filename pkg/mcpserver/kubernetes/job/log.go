package job

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8s "k8s.io/client-go/kubernetes"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

// maxLogPods bounds the attempts job_log reads per job; the newest attempts explain the current state.
const maxLogPods = 3

type logInput struct {
	Jobs      []kube.ObjectRef `json:"jobs" jsonschema:"the jobs whose logs to show, at most 5"`
	Container string           `json:"container,omitempty" jsonschema:"only this container; omit for every main container"`
	TailLines int              `json:"tail_lines,omitempty" jsonschema:"how many recent lines per container (default 50, at most 500)"`
	Previous  bool             `json:"previous,omitempty" jsonschema:"show each container's previous, restarted instance"`
}

// JobLogOutput is the structured result of job_log: one entry per requested job, in request order.
type JobLogOutput struct {
	Jobs []JobLog `json:"jobs"`
}

// JobLog is one job's newest pods' output; Total counts every pod the job owns.
type JobLog struct {
	Namespace string   `json:"namespace"`
	Name      string   `json:"name"`
	Found     bool     `json:"found"`
	Note      string   `json:"note,omitempty"`
	Health    string   `json:"health,omitempty"`
	Previous  bool     `json:"previous,omitempty"`
	Total     int      `json:"total_pods,omitempty"`
	Pods      []PodLog `json:"pods,omitempty"`
}

// PodLog is one attempt's container output; Note explains a container the pod does not have.
type PodLog struct {
	Pod        string              `json:"pod"`
	Note       string              `json:"note,omitempty"`
	Containers []kube.ContainerLog `json:"containers,omitempty"`
}

func logHandler(c kube.Clients) mcp.ToolHandlerFor[logInput, JobLogOutput] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in logInput) (*mcp.CallToolResult, JobLogOutput, error) {
		// Options are per call; the handler itself is shared by concurrent requests.
		var opts kube.LogOptions
		return kube.PerTarget(
			func(in logInput) ([]kube.ObjectRef, error) {
				var err error
				if opts, err = kube.NewLogOptions(in.Container, in.TailLines, in.Previous); err != nil {
					return nil, err
				}
				return kube.ObjectTargets("jobs", in.Jobs, kube.MaxLogTargets)
			},
			func(ctx context.Context, ref kube.ObjectRef) (JobLog, error) { return getLog(ctx, c.Core, ref, opts) },
			formatLog,
			func(j []JobLog) JobLogOutput { return JobLogOutput{Jobs: j} },
		)(ctx, req, in)
	}
}

func getLog(ctx context.Context, client k8s.Interface, ref kube.ObjectRef, opts kube.LogOptions) (JobLog, error) {
	out := JobLog{Namespace: ref.Namespace, Name: ref.Name, Previous: opts.Previous}
	j, note, err := getJob(ctx, client, ref)
	if err != nil || j == nil {
		out.Note = note
		return out, err
	}
	selector, err := metav1.LabelSelectorAsSelector(j.Spec.Selector)
	if err != nil {
		return JobLog{}, err
	}
	pods, err := kube.ListOwnedPods(ctx, client, j.Namespace, selector, map[types.UID]bool{j.UID: true})
	if err != nil {
		return JobLog{}, err
	}
	sort.Slice(pods, func(a, b int) bool { return pods[a].CreationTimestamp.After(pods[b].CreationTimestamp.Time) })
	out.Found, out.Health, out.Total = true, health(j), len(pods)
	for i := range pods[:min(len(pods), maxLogPods)] {
		logs, note, err := kube.PodLogs(ctx, client, &pods[i], opts)
		if err != nil {
			return JobLog{}, err
		}
		out.Pods = append(out.Pods, PodLog{Pod: pods[i].Name, Note: note, Containers: logs})
	}
	return out, nil
}

func formatLog(out JobLog) string {
	target := out.Namespace + "/" + out.Name
	if !out.Found {
		return kube.Miss("job", target, out.Note)
	}
	head := fmt.Sprintf("%s %s (%d pods", target, out.Health, out.Total)
	if out.Total > len(out.Pods) {
		head += fmt.Sprintf(", showing the newest %d", len(out.Pods))
	}
	head += ")"
	if len(out.Pods) == 0 {
		return head + "\nno pods to read logs from"
	}
	blocks := []string{head}
	for _, p := range out.Pods {
		podTarget := out.Namespace + "/" + p.Pod
		if p.Note != "" {
			blocks = append(blocks, fmt.Sprintf("pod %q: %s", podTarget, p.Note))
			continue
		}
		blocks = append(blocks, kube.FormatContainerLogs(podTarget, p.Containers, out.Previous))
	}
	return strings.Join(blocks, "\n\n")
}
