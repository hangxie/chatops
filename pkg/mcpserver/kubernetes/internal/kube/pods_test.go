package kube

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

func Test_podStatus(t *testing.T) {
	sidecar := corev1.Container{Name: "proxy", RestartPolicy: kubetest.Ptr(corev1.ContainerRestartPolicyAlways)}
	tests := map[string]struct {
		pod  *corev1.Pod
		want string
	}{
		"running":       {pod: kubetest.WorkloadPod("ns", "p", "a"), want: "Running"},
		"pending":       {pod: kubetest.WorkloadPod("ns", "p", "a", func(p *corev1.Pod) { p.Status = corev1.PodStatus{Phase: corev1.PodPending} }), want: "Pending"},
		"pod reason":    {pod: kubetest.WorkloadPod("ns", "p", "a", func(p *corev1.Pod) { p.Status = corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted"} }), want: "Evicted"},
		"crash loop":    {pod: kubetest.WorkloadPod("ns", "p", "a", kubetest.WithContainer(kubetest.Waiting("CrashLoopBackOff"), false, 12)), want: "CrashLoopBackOff"},
		"image pull":    {pod: kubetest.WorkloadPod("ns", "p", "a", kubetest.WithContainer(kubetest.Waiting("ImagePullBackOff"), false, 0)), want: "ImagePullBackOff"},
		"oom killed":    {pod: kubetest.WorkloadPod("ns", "p", "a", kubetest.WithContainer(kubetest.Terminated("OOMKilled", 137, 0), false, 3)), want: "OOMKilled"},
		"signal":        {pod: kubetest.WorkloadPod("ns", "p", "a", kubetest.WithContainer(kubetest.Terminated("", 0, 9), false, 0)), want: "Signal:9"},
		"exit code":     {pod: kubetest.WorkloadPod("ns", "p", "a", kubetest.WithContainer(kubetest.Terminated("", 2, 0), false, 0)), want: "ExitCode:2"},
		"blank waiting": {pod: kubetest.WorkloadPod("ns", "p", "a", kubetest.WithContainer(kubetest.Waiting(""), false, 0)), want: "Running"},
		"init running":  {pod: kubetest.WorkloadPod("ns", "p", "a", kubetest.WithInit(corev1.Container{Name: "init"}, kubetest.Running())), want: "Init:0/1"},
		"init failed":   {pod: kubetest.WorkloadPod("ns", "p", "a", kubetest.WithInit(corev1.Container{Name: "init"}, kubetest.Terminated("Error", 1, 0))), want: "Init:Error"},
		"init waiting":  {pod: kubetest.WorkloadPod("ns", "p", "a", kubetest.WithInit(corev1.Container{Name: "init"}, kubetest.Waiting("CrashLoopBackOff"))), want: "Init:CrashLoopBackOff"},
		"init starting": {pod: kubetest.WorkloadPod("ns", "p", "a", kubetest.WithInit(corev1.Container{Name: "init"}, kubetest.Waiting("PodInitializing"))), want: "Init:0/1"},
		"init done":     {pod: kubetest.WorkloadPod("ns", "p", "a", kubetest.WithInit(corev1.Container{Name: "init"}, kubetest.Terminated("Completed", 0, 0))), want: "Running"},
		"sidecar":       {pod: kubetest.WorkloadPod("ns", "p", "a", kubetest.WithInit(sidecar, kubetest.Running())), want: "Running"},
		"second init":   {pod: kubetest.WorkloadPod("ns", "p", "a", kubetest.WithInit(corev1.Container{Name: "one"}, kubetest.Terminated("Completed", 0, 0)), kubetest.WithInit(corev1.Container{Name: "two"}, kubetest.Running())), want: "Init:1/2"},
		"terminating":   {pod: kubetest.WorkloadPod("ns", "p", "a", func(p *corev1.Pod) { p.DeletionTimestamp = kubetest.Ptr(metav1.Now()) }), want: "Terminating"},
		"node lost":     {pod: kubetest.WorkloadPod("ns", "p", "a", func(p *corev1.Pod) { p.DeletionTimestamp = kubetest.Ptr(metav1.Now()); p.Status.Reason = "NodeLost" }), want: "Unknown"},
		"first wins": {pod: kubetest.WorkloadPod("ns", "p", "a", func(p *corev1.Pod) {
			p.Status.ContainerStatuses = []corev1.ContainerStatus{{State: kubetest.Waiting("CrashLoopBackOff")}, {State: kubetest.Waiting("ImagePullBackOff")}}
		}), want: "CrashLoopBackOff"},
		"unknown sidecar": {pod: kubetest.WorkloadPod("ns", "p", "a", func(p *corev1.Pod) {
			p.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "ghost", State: kubetest.Running(), Started: kubetest.Ptr(true)}}
		}), want: "Init:0/0"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, podStatus(tc.pod))
		})
	}
}

func Test_summarizePod(t *testing.T) {
	created := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	p := kubetest.WorkloadPod("ns", "web-1", "web", func(p *corev1.Pod) {
		p.CreationTimestamp = metav1.NewTime(created)
		p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: "sidecar"})
		p.Status.ContainerStatuses = []corev1.ContainerStatus{
			{Name: "main", Ready: true, RestartCount: 2, State: kubetest.Running()},
			{Name: "sidecar", Ready: false, RestartCount: 3, State: kubetest.Waiting("CrashLoopBackOff")},
		}
	})
	require.Equal(t, PodSummary{
		Name: "web-1", Ready: 1, Containers: 2, Status: "CrashLoopBackOff", Restarts: 5,
		Node: "worker-0", Created: created,
	}, SummarizePod(p))
}

func Test_summarizePod_native_sidecar(t *testing.T) {
	sidecar := corev1.Container{Name: "proxy", RestartPolicy: kubetest.Ptr(corev1.ContainerRestartPolicyAlways)}
	tests := map[string]struct {
		sidecar  corev1.ContainerStatus
		ready    int
		restarts int32
		status   string
	}{
		"healthy": {sidecar: corev1.ContainerStatus{Ready: true, Started: kubetest.Ptr(true), State: kubetest.Running()}, ready: 2, status: "Running"},
		"failing": {
			sidecar: corev1.ContainerStatus{RestartCount: 5, Started: kubetest.Ptr(false), State: kubetest.Waiting("CrashLoopBackOff")},
			ready:   1, restarts: 5, status: "Init:CrashLoopBackOff",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			s := SummarizePod(kubetest.WorkloadPod("ns", "p", "a", kubetest.WithInitStatus(sidecar, tc.sidecar)))
			require.Equal(t, 2, s.Containers)
			require.Equal(t, tc.ready, s.Ready)
			require.Equal(t, tc.restarts, s.Restarts)
			require.Equal(t, tc.status, s.Status)
		})
	}
}

func Test_summarizePod_init_restarts(t *testing.T) {
	tests := map[string]struct {
		init     corev1.ContainerStatus
		restarts int32
	}{
		"initializing": {init: corev1.ContainerStatus{RestartCount: 2, State: kubetest.Waiting("CrashLoopBackOff")}, restarts: 2},
		"initialized":  {init: corev1.ContainerStatus{RestartCount: 2, State: kubetest.Terminated("Completed", 0, 0)}, restarts: 0},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			// A regular init container never counts in READY, and its restarts drop out once the pod has initialized.
			s := SummarizePod(kubetest.WorkloadPod("ns", "p", "a", kubetest.WithInitStatus(corev1.Container{Name: "init"}, tc.init)))
			require.Equal(t, 1, s.Containers)
			require.Equal(t, tc.restarts, s.Restarts)
		})
	}
}

func Test_formatPodTable(t *testing.T) {
	created := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	kubetest.FreezeNow(t, &Now, created.Add(2*time.Hour))
	pods := []PodSummary{
		{Name: "web-1", Ready: 1, Containers: 1, Status: "Running", Node: "worker-2", Created: created, Revision: "current"},
		{Name: "web-2", Ready: 0, Containers: 1, Status: "Pending", Restarts: 12, Created: created, Revision: "old"},
	}

	text := FormatPodTable(pods)
	require.Regexp(t, `NAME\s+READY\s+STATUS\s+RESTARTS\s+NODE\s+AGE\s+REVISION`, text)
	require.Regexp(t, `web-1\s+1/1\s+Running\s+0\s+worker-2\s+2h\s+current`, text)
	require.Regexp(t, `web-2\s+0/1\s+Pending\s+12\s+n/a\s+2h\s+old`, text)

	pods[0].Revision, pods[1].Revision = "", ""
	require.NotContains(t, FormatPodTable(pods), "REVISION")
}
