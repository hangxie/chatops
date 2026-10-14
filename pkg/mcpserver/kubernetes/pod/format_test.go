package pod

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

func Test_formatPodList(t *testing.T) {
	created := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	kubetest.FreezeNow(t, &kube.Now, created.Add(2*time.Hour))
	out := PodListOutput{Pods: []PodListItem{
		{Namespace: "argocd", Name: "server-1", Ready: 1, Containers: 1, Status: "Running", Node: "worker-0", Owner: "ReplicaSet/server-abc", Created: created},
		{Namespace: "argocd", Name: "bare", Containers: 1, Status: "Pending", Restarts: 3, Created: created},
	}}

	text := formatPodList(out)
	require.Regexp(t, `NAMESPACE\s+NAME\s+READY\s+STATUS\s+RESTARTS\s+NODE\s+AGE\s+OWNER`, text)
	require.Regexp(t, `argocd\s+server-1\s+1/1\s+Running\s+0\s+worker-0\s+2h\s+ReplicaSet/server-abc`, text)
	require.Regexp(t, `argocd\s+bare\s+0/1\s+Pending\s+3\s+n/a\s+2h\s+n/a`, text)
}

func Test_emptyPodList(t *testing.T) {
	tests := map[string]struct {
		out  PodListOutput
		want string
	}{
		"anywhere":     {want: "no pods found"},
		"namespace":    {out: PodListOutput{Namespace: "argocd"}, want: `no pods in namespace "argocd"`},
		"node":         {out: PodListOutput{Node: "worker-4"}, want: `no pods on node "worker-4"`},
		"both filters": {out: PodListOutput{Namespace: "argocd", Node: "worker-4"}, want: `no pods in namespace "argocd" on node "worker-4"`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, formatPodList(tc.out))
		})
	}
}

func Test_formatPodLog(t *testing.T) {
	tests := map[string]struct {
		out  PodLog
		want string
	}{
		"logs":              {out: PodLog{Namespace: "a", Name: "p", Found: true, Containers: []kube.ContainerLog{{Container: "main", Log: "hi"}}}, want: "== a/p [main] ==\nhi"},
		"previous":          {out: PodLog{Namespace: "a", Name: "p", Found: true, Previous: true, Containers: []kube.ContainerLog{{Container: "main", Log: "hi"}}}, want: "== a/p [main] (previous) ==\nhi"},
		"unknown container": {out: PodLog{Namespace: "a", Name: "p", Found: true, Note: `container "x" not found`}, want: `pod "a/p": container "x" not found`},
		"missing":           {out: PodLog{Namespace: "a", Name: "ghost"}, want: `pod "a/ghost" not found`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, formatPodLog(tc.out))
		})
	}
}

func Test_formatPodResources(t *testing.T) {
	out := PodResources{Namespace: "argocd", Name: "server-1", Found: true, UsageNote: "metrics late", Containers: []ContainerResources{
		{Container: "main", Requests: Amounts{CPU: "100m", Memory: "128Mi"}, Limits: Amounts{Memory: "512Mi"}, Usage: &Amounts{CPU: "50m", Memory: "256Mi"}},
		{Container: "proxy"},
	}}

	text := formatPodResources(out)
	require.True(t, len(text) > 0 && text[:len("argocd/server-1\n")] == "argocd/server-1\n")
	// CPU usage is measured against the request when no limit is set, memory against the limit.
	require.Regexp(t, `main\s+100m\s+n/a\s+50m \(50%\)\s+128Mi\s+512Mi\s+256Mi \(50%\)`, text)
	require.Regexp(t, `proxy\s+n/a\s+n/a\s+n/a\s+n/a\s+n/a\s+n/a`, text)
	require.Contains(t, text, "\nnote: metrics late")
	require.Equal(t, `pod "argocd/ghost" not found`, formatPodResources(PodResources{Namespace: "argocd", Name: "ghost"}))
}

func Test_humanMemory(t *testing.T) {
	require.Equal(t, "128Mi", humanMemory("128Mi"))
	require.Equal(t, "1024Mi", humanMemory("1Gi"))
	require.Equal(t, "", humanMemory(""))
	require.Equal(t, "bad", humanMemory("bad"))
}
