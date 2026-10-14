package kube

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

// logClient answers log requests from logs keyed by container; an error value is returned as the API error.
func logClient(t *testing.T, logs map[string]any) (*fake.Clientset, *[]corev1.PodLogOptions) {
	t.Helper()
	client := fake.NewClientset()
	var seen []corev1.PodLogOptions
	client.PrependReactor("get", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() != "log" {
			return false, nil, nil
		}
		opts := a.(k8stesting.GenericAction).GetValue().(*corev1.PodLogOptions)
		seen = append(seen, *opts)
		switch v := logs[opts.Container].(type) {
		case error:
			return true, nil, v
		case string:
			return true, &runtime.Unknown{Raw: []byte(v)}, nil
		}
		return true, &runtime.Unknown{}, nil
	})
	return client, &seen
}

func logPod() *corev1.Pod {
	return kubetest.WorkloadPod("ns", "web-1", "web", func(p *corev1.Pod) {
		p.Spec.Containers = []corev1.Container{{Name: "main"}, {Name: "proxy"}}
		p.Spec.InitContainers = []corev1.Container{{Name: "init"}}
	})
}

func Test_NewLogOptions(t *testing.T) {
	tests := map[string]struct {
		tail   int
		want   int64
		errMsg string
	}{
		"default":  {tail: 0, want: DefaultTailLines},
		"explicit": {tail: 10, want: 10},
		"maximum":  {tail: MaxTailLines, want: MaxTailLines},
		"too many": {tail: MaxTailLines + 1, errMsg: "tail_lines must be between 1 and 500"},
		"negative": {tail: -1, errMsg: "tail_lines must be between 1 and 500"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			opts, err := NewLogOptions(" main ", tc.tail, true)
			if tc.errMsg != "" {
				require.EqualError(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Equal(t, LogOptions{Container: "main", TailLines: tc.want, Previous: true}, opts)
		})
	}
}

func Test_PodLogs(t *testing.T) {
	client, seen := logClient(t, map[string]any{
		"main":  "line 1\nline 2\n",
		"proxy": apierrors.NewBadRequest(`previous terminated container "proxy" in pod "web-1" not found`),
		"init":  "init done\n",
	})
	tests := map[string]struct {
		opts LogOptions
		want []ContainerLog
		note string
	}{
		"every main container": {opts: LogOptions{TailLines: 50}, want: []ContainerLog{
			{Container: "main", Log: "line 1\nline 2"},
			{Container: "proxy", Note: `previous terminated container "proxy" in pod "web-1" not found`},
		}},
		"named init container": {opts: LogOptions{Container: "init", TailLines: 5}, want: []ContainerLog{{Container: "init", Log: "init done"}}},
		"unknown container":    {opts: LogOptions{Container: "ghost"}, note: `container "ghost" not found (containers: main, proxy, init)`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			logs, note, err := PodLogs(context.Background(), client, logPod(), tc.opts)
			require.NoError(t, err)
			require.Equal(t, tc.want, logs)
			require.Equal(t, tc.note, note)
		})
	}

	*seen = nil
	_, _, err := PodLogs(context.Background(), client, logPod(), LogOptions{Container: "main", TailLines: 7, Previous: true})
	require.NoError(t, err)
	require.Len(t, *seen, 1)
	require.Equal(t, int64(7), *(*seen)[0].TailLines)
	require.True(t, (*seen)[0].Previous)
	require.Nil(t, (*seen)[0].LimitBytes) // a server-side byte limit would cut the newest output, not the oldest
}

func Test_PodLogs_keeps_newest_of_large_output(t *testing.T) {
	// More than the old 1 MiB server-side limit, with the newest line last.
	huge := strings.Repeat(strings.Repeat("x", 99)+"\n", 20*1024) + "NEWEST LINE\n"
	client, _ := logClient(t, map[string]any{"main": huge})

	logs, _, err := PodLogs(context.Background(), client, logPod(), LogOptions{Container: "main", TailLines: 500})
	require.NoError(t, err)
	require.True(t, logs[0].Truncated)
	require.True(t, strings.HasSuffix(logs[0].Log, "\nNEWEST LINE"))
	require.LessOrEqual(t, len(logs[0].Log), maxLogBytes)
	require.True(t, strings.HasPrefix(logs[0].Log, strings.Repeat("x", 99)), "starts at a line boundary")
}

func Test_tailBuffer(t *testing.T) {
	tb := &tailBuffer{limit: 4}
	for _, chunk := range []string{"ab", "cdef", "ghij"} {
		n, err := tb.Write([]byte(chunk))
		require.NoError(t, err)
		require.Equal(t, len(chunk), n)
	}
	require.Equal(t, "cdefghij", string(tb.buf)) // the last 2*limit bytes
	require.True(t, tb.dropped)

	small := &tailBuffer{limit: 100}
	_, _ = small.Write([]byte("short"))
	require.False(t, small.dropped)
}

func Test_PodLogs_error(t *testing.T) {
	client, _ := logClient(t, map[string]any{"main": errors.New("connection refused")})
	_, _, err := PodLogs(context.Background(), client, logPod(), LogOptions{Container: "main", TailLines: 5})
	require.ErrorContains(t, err, "connection refused")
}

func Test_keepTail(t *testing.T) {
	text, truncated := keepTail([]byte("short\n"), 100)
	require.Equal(t, "short", text)
	require.False(t, truncated)

	raw := []byte(strings.Repeat("old line\n", 10) + "newest\n")
	text, truncated = keepTail(raw, 20)
	require.True(t, truncated)
	require.Equal(t, "old line\nnewest", text) // starts at a line boundary, keeps the newest line

	text, truncated = keepTail([]byte(strings.Repeat("x", 50)), 10)
	require.True(t, truncated)
	require.Equal(t, strings.Repeat("x", 10), text) // no newline to align on
}

func Test_statusMessage(t *testing.T) {
	require.Equal(t, "bad", statusMessage(apierrors.NewBadRequest("bad")))
	require.Equal(t, "plain", statusMessage(errors.New("plain")))
}

func Test_FormatContainerLogs(t *testing.T) {
	logs := []ContainerLog{
		{Container: "main", Log: "hello"},
		{Container: "proxy", Note: "container is waiting to start"},
		{Container: "idle"},
		{Container: "chatty", Log: "newest", Truncated: true},
	}
	require.Equal(t, "== ns/web-1 [main] ==\nhello\n\n"+
		"== ns/web-1 [proxy] ==\ncontainer is waiting to start\n\n"+
		"== ns/web-1 [idle] ==\n(no output)\n\n"+
		"== ns/web-1 [chatty] ==\n… (earlier output truncated)\nnewest", FormatContainerLogs("ns/web-1", logs, false))
	require.Equal(t, "== ns/web-1 [main] (previous) ==\nhello", FormatContainerLogs("ns/web-1", logs[:1], true))
}
