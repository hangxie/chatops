package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	restfake "k8s.io/client-go/rest/fake"
	k8stesting "k8s.io/client-go/testing"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

func fixedDisk(used, total int64) func(context.Context, string) (int64, int64, bool, error) {
	return func(context.Context, string) (int64, int64, bool, error) { return used, total, true, nil }
}

func resourceList(cpu, memory, disk, pods string) corev1.ResourceList {
	return corev1.ResourceList{
		corev1.ResourceCPU:              resource.MustParse(cpu),
		corev1.ResourceMemory:           resource.MustParse(memory),
		corev1.ResourceEphemeralStorage: resource.MustParse(disk),
		corev1.ResourcePods:             resource.MustParse(pods),
	}
}

func pod(name, nodeName string, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       corev1.PodSpec{NodeName: nodeName},
		Status:     corev1.PodStatus{Phase: phase},
	}
}

func nodeMetrics(name, cpu, memory string) *metricsv1beta1.NodeMetrics {
	return &metricsv1beta1.NodeMetrics{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Usage: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(cpu),
			corev1.ResourceMemory: resource.MustParse(memory),
		},
	}
}

// newMetricsFake registers node metrics under the real "nodes" resource; the default auto-add guesses
// a resource name the typed client never queries, so Get would otherwise miss them.
func newMetricsFake(objs ...*metricsv1beta1.NodeMetrics) *metricsfake.Clientset {
	cs := metricsfake.NewSimpleClientset()
	gvr := metricsv1beta1.SchemeGroupVersion.WithResource("nodes")
	for _, o := range objs {
		if err := cs.Tracker().Create(gvr, o, ""); err != nil {
			panic(err)
		}
	}
	return cs
}

func Test_getNodeResources(t *testing.T) {
	core := fake.NewClientset(
		node("worker-0", capacity(resourceList("16", "32Gi", "100Gi", "110")),
			allocatable(resourceList("16", "31Gi", "94Gi", "110"))),
		pod("a", "worker-0", corev1.PodRunning),
		pod("b", "worker-0", corev1.PodRunning),
		pod("c", "worker-0", corev1.PodSucceeded), // terminated, excluded
		pod("d", "other", corev1.PodRunning),      // different node, excluded
	)
	metrics := newMetricsFake(nodeMetrics("worker-0", "4", "8Gi"))
	c := clients{core: core, metrics: metrics, diskUsage: fixedDisk(10737418240, 107374182400)}

	out, err := getNodeResources(context.Background(), c, "worker-0")
	require.NoError(t, err)
	require.True(t, out.Found)
	require.Equal(t, "16", out.Capacity.CPU)
	require.Equal(t, "100Gi", out.Capacity.Disk)
	require.Equal(t, "110", out.Capacity.Pods)
	require.Equal(t, "31Gi", out.Allocatable.Memory)
	require.Equal(t, "4", out.Usage.CPU)
	require.Equal(t, "8Gi", out.Usage.Memory)
	require.Equal(t, "2", out.Usage.Pods)
	require.Equal(t, "10737418240", out.Usage.Disk)
	require.Equal(t, "107374182400", out.DiskTotal) // filesystem total, not ephemeral-storage allocatable
	require.Empty(t, out.UsageNote)
}

func Test_getNodeResources_metrics_unavailable(t *testing.T) {
	core := fake.NewClientset(
		node("worker-0", capacity(resourceList("16", "32Gi", "100Gi", "110")),
			allocatable(resourceList("16", "31Gi", "94Gi", "110"))),
		pod("a", "worker-0", corev1.PodRunning),
	)
	metrics := newMetricsFake() // no node metrics -> Get returns NotFound
	c := clients{core: core, metrics: metrics, diskUsage: fixedDisk(10737418240, 107374182400)}

	out, err := getNodeResources(context.Background(), c, "worker-0")
	require.NoError(t, err)
	require.True(t, out.Found)
	require.Empty(t, out.Usage.CPU)
	require.Empty(t, out.Usage.Memory)
	require.Equal(t, "1", out.Usage.Pods)
	require.Equal(t, "10737418240", out.Usage.Disk)
	require.Contains(t, out.UsageNote, "cpu and memory utilization unavailable")
}

func Test_getNodeResources_disk_degradation(t *testing.T) {
	disk := func(used, total int64, ok bool, err error) func(context.Context, string) (int64, int64, bool, error) {
		return func(context.Context, string) (int64, int64, bool, error) { return used, total, ok, err }
	}
	timeout := fmt.Errorf("Get \"https://api/api/v1/nodes/worker-0/proxy/stats/summary\": %w", context.DeadlineExceeded)
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "nodes/proxy"}, "worker-0", errors.New("RBAC denied"))

	tests := map[string]struct {
		diskUsage func(context.Context, string) (int64, int64, bool, error)
		disk      string
		diskTotal string
		note      string
	}{
		"used and total":     {diskUsage: disk(10737418240, 107374182400, true, nil), disk: "10737418240", diskTotal: "107374182400"},
		"capacity absent":    {diskUsage: disk(10737418240, 0, true, nil), disk: "10737418240"},
		"no node filesystem": {diskUsage: disk(0, 0, false, nil), note: "disk utilization unavailable: kubelet Summary API reported no node filesystem usage"},
		"forbidden":          {diskUsage: disk(0, 0, false, forbidden), note: "disk utilization not permitted: grant get on nodes/proxy to read the kubelet Summary API"},
		"timeout":            {diskUsage: disk(0, 0, false, timeout), note: "disk utilization unavailable: " + timeout.Error()},
		"other error":        {diskUsage: disk(0, 0, false, errors.New("kubelet unreachable")), note: "disk utilization unavailable: kubelet unreachable"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			core := fake.NewClientset(node("worker-0"), pod("a", "worker-0", corev1.PodRunning))
			c := clients{core: core, metrics: newMetricsFake(nodeMetrics("worker-0", "4", "8Gi")), diskUsage: tc.diskUsage}

			out, err := getNodeResources(context.Background(), c, "worker-0")
			require.NoError(t, err)
			require.True(t, out.Found)
			require.Equal(t, tc.disk, out.Usage.Disk)
			require.Equal(t, tc.diskTotal, out.DiskTotal)
			require.Equal(t, tc.note, out.UsageNote)
			// Disk trouble never costs the other figures.
			require.Equal(t, "4", out.Usage.CPU)
			require.Equal(t, "8Gi", out.Usage.Memory)
			require.Equal(t, "1", out.Usage.Pods)
		})
	}
}

func Test_getNodeResources_not_found(t *testing.T) {
	c := clients{core: fake.NewClientset(), metrics: newMetricsFake(), diskUsage: fixedDisk(0, 0)}

	out, err := getNodeResources(context.Background(), c, "ghost")
	require.NoError(t, err)
	require.False(t, out.Found)
	require.Nil(t, out.Capacity)
	require.Nil(t, out.Usage)
}

func Test_getNodeResources_node_error(t *testing.T) {
	core := fake.NewClientset()
	core.PrependReactor("get", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("TLS handshake timeout")
	})

	_, err := getNodeResources(context.Background(), clients{core: core, metrics: newMetricsFake()}, "worker-0")
	require.EqualError(t, err, "TLS handshake timeout")
}

func Test_getNodeResources_pods_error(t *testing.T) {
	core := fake.NewClientset(node("worker-0"))
	core.PrependReactor("list", "pods", kubetest.Forbidden("list", "pods"))
	c := clients{core: core, metrics: newMetricsFake(nodeMetrics("worker-0", "4", "8Gi")), diskUsage: fixedDisk(0, 0)}

	out, err := getNodeResources(context.Background(), c, "worker-0")
	require.NoError(t, err)
	require.True(t, out.Found)
	require.Equal(t, "4", out.Usage.CPU) // metrics still returned despite the pod-list failure
	require.Empty(t, out.Usage.Pods)
	require.Contains(t, out.UsageNote, "pod count unavailable")
}

func Test_handlers_surface_errors(t *testing.T) {
	core := fake.NewClientset()
	core.PrependReactor("list", "nodes", kubetest.Forbidden("list", "nodes"))
	core.PrependReactor("get", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("boom")
	})
	c := clients{core: core, metrics: newMetricsFake()}

	_, _, err := nodeListHandler(c)(context.Background(), nil, struct{}{})
	require.Error(t, err)
	_, _, err = nodeStatusHandler(c)(context.Background(), nil, nodeInput{Nodes: []string{"worker-0"}})
	require.Error(t, err)
	_, _, err = nodeResourcesHandler(c)(context.Background(), nil, nodeInput{Nodes: []string{"worker-0"}})
	require.Error(t, err)
}

func summaryClient(t *testing.T, status int, body string) *restfake.RESTClient {
	t.Helper()
	return &restfake.RESTClient{
		NegotiatedSerializer: scheme.Codecs.WithoutConversion(),
		GroupVersion:         corev1.SchemeGroupVersion,
		Client: restfake.CreateHTTPClient(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: status,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}),
	}
}

func Test_summaryDiskUsage(t *testing.T) {
	rc := summaryClient(t, http.StatusOK, `{"node":{"fs":{"usedBytes":10737418240,"capacityBytes":107374182400}}}`)
	used, total, ok, err := summaryDiskUsage(rc)(context.Background(), "worker-0")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int64(10737418240), used)
	require.Equal(t, int64(107374182400), total)
}

func Test_summaryDiskUsage_no_capacity(t *testing.T) {
	rc := summaryClient(t, http.StatusOK, `{"node":{"fs":{"usedBytes":10737418240}}}`)
	used, total, ok, err := summaryDiskUsage(rc)(context.Background(), "worker-0")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int64(10737418240), used)
	require.Zero(t, total)
}

func Test_summaryDiskUsage_zero_capacity(t *testing.T) {
	rc := summaryClient(t, http.StatusOK, `{"node":{"fs":{"usedBytes":10737418240,"capacityBytes":0}}}`)
	used, total, ok, err := summaryDiskUsage(rc)(context.Background(), "worker-0")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int64(10737418240), used)
	require.Zero(t, total)
}

func Test_summaryDiskUsage_no_fs(t *testing.T) {
	rc := summaryClient(t, http.StatusOK, `{"node":{"fs":{}}}`)
	_, _, ok, err := summaryDiskUsage(rc)(context.Background(), "worker-0")
	require.NoError(t, err)
	require.False(t, ok)
}

func Test_summaryDiskUsage_forbidden(t *testing.T) {
	rc := summaryClient(t, http.StatusForbidden, "forbidden")
	_, _, _, err := summaryDiskUsage(rc)(context.Background(), "worker-0")
	require.True(t, apierrors.IsForbidden(err), err)
}

func Test_summaryDiskUsage_bad_json(t *testing.T) {
	rc := summaryClient(t, http.StatusOK, "not json")
	_, _, _, err := summaryDiskUsage(rc)(context.Background(), "worker-0")
	require.Error(t, err)
}
