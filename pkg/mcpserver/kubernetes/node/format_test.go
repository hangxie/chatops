package node

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

func Test_formatNodeList(t *testing.T) {
	created := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	kubetest.FreezeNow(t, &kube.Now, created.Add(32*24*time.Hour))
	out := NodeListOutput{Nodes: []NodeListItem{
		{Name: "master-0", Ready: "Ready", Roles: []string{"control-plane"}, Created: created, Version: "v1.37.1"},
		{Name: "worker-0", Ready: "NotReady", Roles: nil, Created: created, Version: "v1.37.1"},
	}}

	text := formatNodeList(out)
	require.Contains(t, text, "NAME")
	require.Contains(t, text, "STATUS")
	require.Regexp(t, `master-0\s+Ready\s+control-plane\s+32d\s+v1\.37\.1`, text)
	require.Regexp(t, `worker-0\s+NotReady\s+<none>\s+32d\s+v1\.37\.1`, text)
}

func Test_formatNodeList_empty(t *testing.T) {
	require.Equal(t, "no nodes found", formatNodeList(NodeListOutput{}))
}

func Test_formatNodeStatus(t *testing.T) {
	out := NodeStatus{
		Node: "worker-4", Found: true, Ready: "Ready", Roles: nil, Version: "v1.37.1",
		Conditions:  map[string]string{"Ready": "True", "MemoryPressure": "False", "DiskPressure": "Unknown"},
		Schedulable: kubetest.Ptr(false),
		Taints:      []string{"gpu:NoSchedule"},
	}
	text := formatNodeStatus(out)
	require.Contains(t, text, "[Ready] worker-4 (<none>) v1.37.1")
	require.Contains(t, text, "conditions: Ready=True MemoryPressure=False DiskPressure=Unknown")
	require.Contains(t, text, "scheduling: unschedulable taints=gpu:NoSchedule")
}

func Test_formatNodeStatus_not_found(t *testing.T) {
	require.Equal(t, `node "ghost" not found`, formatNodeStatus(NodeStatus{Node: "ghost"}))
}

func Test_formatNodeResources(t *testing.T) {
	out := NodeResources{
		Node: "worker-0", Found: true,
		Capacity:    &ResourceAmounts{CPU: "16", Memory: "32Gi", Disk: "100Gi", Pods: "110"},
		Allocatable: &ResourceAmounts{CPU: "16", Memory: "31Gi", Disk: "94Gi", Pods: "110"},
		Usage:       &ResourceAmounts{CPU: "4", Memory: "8Gi", Disk: "10737418240", Pods: "2"},
		DiskTotal:   "107374182400", // filesystem total, not ephemeral-storage allocatable
	}
	text := formatNodeResources(out)
	require.Contains(t, text, "capacity:    cpu=16 memory=32.0Gi disk=100.0Gi pods=110")
	require.Contains(t, text, "cpu=4000m (25%)")
	require.Contains(t, text, "memory=8.0Gi (26%)")
	require.Contains(t, text, "disk=10.0Gi (10%)") // 10Gi of a 100Gi filesystem
	require.Contains(t, text, "pods=2 (2%)")
}

func Test_formatNodeResources_metrics_unavailable(t *testing.T) {
	out := NodeResources{
		Node: "worker-0", Found: true,
		Capacity:    &ResourceAmounts{CPU: "16", Memory: "32Gi", Disk: "100Gi", Pods: "110"},
		Allocatable: &ResourceAmounts{CPU: "16", Memory: "31Gi", Disk: "94Gi", Pods: "110"},
		Usage:       &ResourceAmounts{Pods: "5"},
		UsageNote:   "cpu and memory utilization unavailable: metrics not found",
	}
	text := formatNodeResources(out)
	require.Contains(t, text, "cpu=n/a memory=n/a disk=n/a pods=5")
	require.Contains(t, text, "note: cpu and memory utilization unavailable")
}

func Test_formatNodeResources_disk_without_total(t *testing.T) {
	out := NodeResources{
		Node: "worker-0", Found: true,
		Allocatable: &ResourceAmounts{CPU: "16", Memory: "31Gi", Disk: "94Gi", Pods: "110"},
		Usage:       &ResourceAmounts{CPU: "4", Memory: "8Gi", Disk: "10737418240", Pods: "2"},
	}
	// No filesystem total: show used bytes without a percentage rather than one against ephemeral-storage.
	require.Contains(t, formatNodeResources(out), "disk=10.0Gi pods=2 (2%)")
}

func Test_formatNodeResources_not_found(t *testing.T) {
	require.Equal(t, `node "ghost" not found`, formatNodeResources(NodeResources{Node: "ghost"}))
}

func Test_conditionsText_order(t *testing.T) {
	m := map[string]string{"Ready": "True", "DiskPressure": "False", "NetworkUnavailable": "False", "FooBar": "True"}
	require.Equal(t, "Ready=True DiskPressure=False NetworkUnavailable=False FooBar=True", conditionsText(m))
	require.Equal(t, "(none)", conditionsText(nil))
}

func Test_rolesText(t *testing.T) {
	require.Equal(t, "<none>", rolesText(nil))
	require.Equal(t, "control-plane,worker", rolesText([]string{"control-plane", "worker"}))
}

func Test_amountsText_nil(t *testing.T) {
	require.Equal(t, "(unknown)", amountsText(nil))
}

func Test_usageText_nil(t *testing.T) {
	require.Equal(t, "(unavailable)", usageText(NodeResources{}))
}

func Test_allocField(t *testing.T) {
	a := &ResourceAmounts{CPU: "1", Memory: "2", Pods: "3"}
	require.Equal(t, "1", allocField(a, "cpu"))
	require.Equal(t, "2", allocField(a, "memory"))
	require.Equal(t, "3", allocField(a, "pods"))
	require.Equal(t, "", allocField(a, "disk"))
	require.Equal(t, "", allocField(a, "bogus"))
	require.Equal(t, "", allocField(nil, "cpu"))
}

func Test_humanSize(t *testing.T) {
	require.Equal(t, "31.3Gi", humanSize("32866976Ki"))
	require.Equal(t, "", humanSize(""))
	require.Equal(t, "not-a-size", humanSize("not-a-size"))
}
