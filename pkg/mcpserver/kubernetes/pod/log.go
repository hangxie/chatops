package pod

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	k8s "k8s.io/client-go/kubernetes"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

type podLogInput struct {
	Pods      []kube.ObjectRef `json:"pods" jsonschema:"the pods whose logs to show, at most 5"`
	Container string           `json:"container,omitempty" jsonschema:"only this container; omit for every main container"`
	TailLines int              `json:"tail_lines,omitempty" jsonschema:"how many recent lines per container (default 50, at most 500)"`
	Previous  bool             `json:"previous,omitempty" jsonschema:"show the previous, crashed container instance"`
}

// PodLogOutput is the structured result of pod_log: one entry per requested pod, in request order.
type PodLogOutput struct {
	Pods []PodLog `json:"pods"`
}

// PodLog is one pod's container output; Note explains a miss or a container the pod does not have.
type PodLog struct {
	Namespace  string              `json:"namespace"`
	Name       string              `json:"name"`
	Found      bool                `json:"found"`
	Note       string              `json:"note,omitempty"`
	Previous   bool                `json:"previous,omitempty"`
	Containers []kube.ContainerLog `json:"containers,omitempty"`
}

func podLogHandler(c kube.Clients) mcp.ToolHandlerFor[podLogInput, PodLogOutput] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in podLogInput) (*mcp.CallToolResult, PodLogOutput, error) {
		// Options are per call; the handler itself is shared by concurrent requests.
		var opts kube.LogOptions
		return kube.PerTarget(
			func(in podLogInput) ([]kube.ObjectRef, error) {
				var err error
				if opts, err = kube.NewLogOptions(in.Container, in.TailLines, in.Previous); err != nil {
					return nil, err
				}
				return kube.ObjectTargets("pods", in.Pods, kube.MaxLogTargets)
			},
			func(ctx context.Context, ref kube.ObjectRef) (PodLog, error) {
				return getPodLog(ctx, c.Core, ref, opts)
			},
			formatPodLog,
			func(p []PodLog) PodLogOutput { return PodLogOutput{Pods: p} },
		)(ctx, req, in)
	}
}

func getPodLog(ctx context.Context, client k8s.Interface, ref kube.ObjectRef, opts kube.LogOptions) (PodLog, error) {
	out := PodLog{Namespace: ref.Namespace, Name: ref.Name, Previous: opts.Previous}
	p, note, err := getPod(ctx, client, ref)
	if err != nil || p == nil {
		out.Note = note
		return out, err
	}
	out.Found = true
	out.Containers, out.Note, err = kube.PodLogs(ctx, client, p, opts)
	return out, err
}
