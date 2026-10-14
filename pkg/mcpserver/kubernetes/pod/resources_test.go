package pod

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

// newMetricsFake registers pod metrics under the real "pods" resource, which the typed client queries.
func newMetricsFake(objs ...*metricsv1beta1.PodMetrics) *metricsfake.Clientset {
	cs := metricsfake.NewSimpleClientset()
	gvr := metricsv1beta1.SchemeGroupVersion.WithResource("pods")
	for _, o := range objs {
		if err := cs.Tracker().Create(gvr, o, o.Namespace); err != nil {
			panic(err)
		}
	}
	return cs
}

func podMetrics(ns, name string, usage map[string][2]string) *metricsv1beta1.PodMetrics {
	m := &metricsv1beta1.PodMetrics{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
	for container, u := range usage {
		m.Containers = append(m.Containers, metricsv1beta1.ContainerMetrics{Name: container, Usage: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse(u[0]), corev1.ResourceMemory: resource.MustParse(u[1]),
		}})
	}
	return m
}

func resourcePod() *corev1.Pod {
	return kubetest.WorkloadPod("argocd", "server-1", "server", func(p *corev1.Pod) {
		p.Spec.Containers[0].Resources = corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")},
			Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
		}
		p.Spec.InitContainers = []corev1.Container{
			{Name: "proxy", RestartPolicy: kubetest.Ptr(corev1.ContainerRestartPolicyAlways)},
			{Name: "setup"},
		}
	})
}

func Test_getPodResources(t *testing.T) {
	c := kube.Clients{
		Core:    fake.NewClientset(kubetest.Namespace("argocd"), resourcePod()),
		Metrics: newMetricsFake(podMetrics("argocd", "server-1", map[string][2]string{"main": {"50m", "256Mi"}, "proxy": {"5m", "10Mi"}})),
	}

	out, err := getPodResources(context.Background(), c, kube.ObjectRef{Namespace: "argocd", Name: "server-1"})
	require.NoError(t, err)
	require.Equal(t, PodResources{Namespace: "argocd", Name: "server-1", Found: true, Containers: []ContainerResources{
		{Container: "proxy", Usage: &Amounts{CPU: "5m", Memory: "10Mi"}},
		{Container: "main", Requests: Amounts{CPU: "100m", Memory: "128Mi"}, Limits: Amounts{Memory: "512Mi"}, Usage: &Amounts{CPU: "50m", Memory: "256Mi"}},
	}}, out)
}

func Test_getPodResources_degrades(t *testing.T) {
	c := kube.Clients{Core: fake.NewClientset(kubetest.Namespace("argocd"), resourcePod()), Metrics: newMetricsFake()}

	out, err := getPodResources(context.Background(), c, kube.ObjectRef{Namespace: "argocd", Name: "server-1"})
	require.NoError(t, err)
	require.True(t, out.Found)
	require.Contains(t, out.UsageNote, "cpu and memory usage unavailable")
	require.Len(t, out.Containers, 2)
	require.Nil(t, out.Containers[1].Usage)
}

func Test_getPodResources_misses_and_errors(t *testing.T) {
	c := kube.Clients{Core: fake.NewClientset(kubetest.Namespace("argocd")), Metrics: newMetricsFake()}
	out, err := getPodResources(context.Background(), c, kube.ObjectRef{Namespace: "nope", Name: "x"})
	require.NoError(t, err)
	require.Equal(t, PodResources{Namespace: "nope", Name: "x", Note: `namespace "nope" does not exist`}, out)

	core := fake.NewClientset()
	core.PrependReactor("get", "pods", kubetest.Forbidden("get", "pods"))
	_, err = getPodResources(context.Background(), kube.Clients{Core: core, Metrics: newMetricsFake()}, kube.ObjectRef{Namespace: "a", Name: "b"})
	require.ErrorContains(t, err, "get denied")
}
