package kubetest

import (
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func Test_WorkloadPod(t *testing.T) {
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-abc", UID: "rs-uid"}}
	p := WorkloadPod("ns", "web-1", "web", OwnedBy(rs), WithContainer(Waiting("CrashLoopBackOff"), false, 3),
		WithInit(corev1.Container{Name: "init"}, Terminated("Completed", 0, 0)),
		WithInit(corev1.Container{Name: "proxy"}, Running()))

	require.Equal(t, "ns", p.Namespace)
	require.Equal(t, map[string]string{"app": "web"}, p.Labels)
	require.Equal(t, "rs-uid", string(metav1.GetControllerOf(p).UID))
	require.Equal(t, corev1.ContainerStatus{Name: "main", RestartCount: 3, State: Waiting("CrashLoopBackOff")}, p.Status.ContainerStatuses[0])
	require.Len(t, p.Spec.InitContainers, 2)
	require.False(t, *p.Status.InitContainerStatuses[0].Started)
	require.True(t, *p.Status.InitContainerStatuses[1].Started)
	require.Equal(t, "proxy", p.Status.InitContainerStatuses[1].Name)
}

func Test_container_states(t *testing.T) {
	require.Equal(t, "ImagePullBackOff", Waiting("ImagePullBackOff").Waiting.Reason)
	term := Terminated("OOMKilled", 137, 9).Terminated
	require.Equal(t, corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137, Signal: 9}, *term)
	require.NotNil(t, Running().Running)
}
