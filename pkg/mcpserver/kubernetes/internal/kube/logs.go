package kube

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8s "k8s.io/client-go/kubernetes"
)

// MaxLogTargets caps every log tool's targets, since each returns raw container output.
const MaxLogTargets = 5

// Log tail bounds: DefaultTailLines when unset, at most MaxTailLines.
const (
	DefaultTailLines = 50
	MaxTailLines     = 500
)

// maxLogBytes keeps each container's output within the agent's tool-result budget.
const maxLogBytes = 16 * 1024

// LogOptions selects which container output a log tool returns.
type LogOptions struct {
	Container string
	TailLines int64
	Previous  bool
}

// ContainerLog is one container's recent output; Note explains output the API could not return.
type ContainerLog struct {
	Container string `json:"container"`
	Log       string `json:"log,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Note      string `json:"note,omitempty"`
}

// NewLogOptions validates a log tool's arguments, defaulting the tail.
func NewLogOptions(container string, tailLines int, previous bool) (LogOptions, error) {
	if tailLines == 0 {
		tailLines = DefaultTailLines
	}
	if tailLines < 0 || tailLines > MaxTailLines {
		return LogOptions{}, fmt.Errorf("tail_lines must be between 1 and %d", MaxTailLines)
	}
	return LogOptions{Container: strings.TrimSpace(container), TailLines: int64(tailLines), Previous: previous}, nil
}

// PodLogs fetches the output of opts.Container, or of every main container when unset; a container the pod does
// not have is a note naming the ones it has, not an error.
func PodLogs(ctx context.Context, client k8s.Interface, p *corev1.Pod, opts LogOptions) ([]ContainerLog, string, error) {
	names := containerNames(p)
	containers := names[:len(p.Spec.Containers)]
	if opts.Container != "" {
		if !contains(names, opts.Container) {
			return nil, fmt.Sprintf("container %q not found (containers: %s)", opts.Container, strings.Join(names, ", ")), nil
		}
		containers = []string{opts.Container}
	}
	logs := make([]ContainerLog, 0, len(containers))
	for _, name := range containers {
		l, err := containerLog(ctx, client, p, name, opts)
		if err != nil {
			return nil, "", err
		}
		logs = append(logs, l)
	}
	return logs, "", nil
}

// containerLog streams the tailed output and keeps only its newest bytes, so a burst of long lines can never push
// the latest output out; tail_lines and the request timeout bound how much is read.
func containerLog(ctx context.Context, client k8s.Interface, p *corev1.Pod, container string, opts LogOptions) (ContainerLog, error) {
	stream, err := client.CoreV1().Pods(p.Namespace).GetLogs(p.Name, &corev1.PodLogOptions{
		Container: container, TailLines: &opts.TailLines, Previous: opts.Previous,
	}).Stream(ctx)
	// The API answers 400 for output that does not exist, such as no previous instance or a container not started.
	if apierrors.IsBadRequest(err) {
		return ContainerLog{Container: container, Note: statusMessage(err)}, nil
	}
	if err != nil {
		return ContainerLog{}, err
	}
	defer func() { _ = stream.Close() }()
	tail := &tailBuffer{limit: maxLogBytes}
	if _, err := io.Copy(tail, stream); err != nil {
		return ContainerLog{}, err
	}
	text, truncated := keepTail(tail.buf, maxLogBytes)
	return ContainerLog{Container: container, Log: text, Truncated: truncated || tail.dropped}, nil
}

// tailBuffer keeps the last limit bytes written plus one line's worth of slack for keepTail to align on.
type tailBuffer struct {
	buf     []byte
	limit   int
	dropped bool
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if keep := 2 * t.limit; len(t.buf) > keep {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-keep:]...)
		t.dropped = true
	}
	return len(p), nil
}

// keepTail keeps the last limit bytes, starting at a line boundary, so the newest output survives.
func keepTail(raw []byte, limit int) (string, bool) {
	raw = bytes.TrimRight(raw, "\n")
	if len(raw) <= limit {
		return string(raw), false
	}
	tail := raw[len(raw)-limit:]
	if i := bytes.IndexByte(tail, '\n'); i >= 0 {
		tail = tail[i+1:]
	}
	return string(tail), true
}

// statusMessage prefers the API's own message over the wrapped transport error text.
func statusMessage(err error) string {
	var status apierrors.APIStatus
	if errors.As(err, &status) && status.Status().Message != "" {
		return status.Status().Message
	}
	return err.Error()
}

// containerNames lists main containers first, then init and ephemeral ones, which can be named explicitly.
func containerNames(p *corev1.Pod) []string {
	names := make([]string, 0, len(p.Spec.Containers)+len(p.Spec.InitContainers)+len(p.Spec.EphemeralContainers))
	for _, c := range p.Spec.Containers {
		names = append(names, c.Name)
	}
	for _, c := range p.Spec.InitContainers {
		names = append(names, c.Name)
	}
	for _, c := range p.Spec.EphemeralContainers {
		names = append(names, c.Name)
	}
	return names
}

func contains(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// FormatContainerLogs renders each container's output under a header naming the pod and container.
func FormatContainerLogs(target string, logs []ContainerLog, previous bool) string {
	blocks := make([]string, 0, len(logs))
	for _, l := range logs {
		head := fmt.Sprintf("== %s [%s]", target, l.Container)
		if previous {
			head += " (previous)"
		}
		head += " =="
		switch {
		case l.Note != "":
			blocks = append(blocks, head+"\n"+l.Note)
		case l.Log == "":
			blocks = append(blocks, head+"\n(no output)")
		case l.Truncated:
			blocks = append(blocks, head+"\n… (earlier output truncated)\n"+l.Log)
		default:
			blocks = append(blocks, head+"\n"+l.Log)
		}
	}
	return strings.Join(blocks, "\n\n")
}
