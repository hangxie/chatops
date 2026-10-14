package kubetest

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PodOpt adjusts a WorkloadPod fixture.
type PodOpt func(*corev1.Pod)

// WorkloadPod builds a running, ready single-container pod in namespace ns labeled app=<app>.
func WorkloadPod(ns, name, app string, opts ...PodOpt) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: map[string]string{"app": app}},
		Spec:       corev1.PodSpec{NodeName: "worker-0", Containers: []corev1.Container{{Name: "main"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{
			{Name: "main", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
		}},
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// OwnedBy makes rs the pod's controller, as the ReplicaSet controller does.
func OwnedBy(rs *appsv1.ReplicaSet) PodOpt {
	return func(p *corev1.Pod) {
		p.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(rs, appsv1.SchemeGroupVersion.WithKind("ReplicaSet"))}
	}
}

// WithContainer replaces the container status.
func WithContainer(st corev1.ContainerState, ready bool, restarts int32) PodOpt {
	return func(p *corev1.Pod) {
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", Ready: ready, RestartCount: restarts, State: st}}
	}
}

// WithInit adds an init container in state st, started when running.
func WithInit(spec corev1.Container, st corev1.ContainerState) PodOpt {
	return WithInitStatus(spec, corev1.ContainerStatus{State: st, Started: Ptr(st.Running != nil)})
}

// WithInitStatus adds an init container with the given status.
func WithInitStatus(spec corev1.Container, cs corev1.ContainerStatus) PodOpt {
	return func(p *corev1.Pod) {
		cs.Name = spec.Name
		p.Spec.InitContainers = append(p.Spec.InitContainers, spec)
		p.Status.InitContainerStatuses = append(p.Status.InitContainerStatuses, cs)
	}
}

// Waiting is a waiting container state.
func Waiting(reason string) corev1.ContainerState {
	return corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason}}
}

// Terminated is a terminated container state.
func Terminated(reason string, code, signal int32) corev1.ContainerState {
	return corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: reason, ExitCode: code, Signal: signal}}
}

// Running is a running container state.
func Running() corev1.ContainerState {
	return corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
}
