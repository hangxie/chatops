package node

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

// test fixtures: a node builder with functional options, shared across the package's tests.

type nodeOpt func(*corev1.Node)

func node(name string, opts ...nodeOpt) *corev1.Node {
	n := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			NodeInfo:   corev1.NodeSystemInfo{KubeletVersion: "v1.37.1"},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
	for _, o := range opts {
		o(n)
	}
	return n
}

func ready(s corev1.ConditionStatus) nodeOpt {
	return func(n *corev1.Node) {
		for i := range n.Status.Conditions {
			if n.Status.Conditions[i].Type == corev1.NodeReady {
				n.Status.Conditions[i].Status = s
				return
			}
		}
	}
}

func role(r string) nodeOpt {
	return func(n *corev1.Node) {
		if n.Labels == nil {
			n.Labels = map[string]string{}
		}
		n.Labels[rolePrefix+r] = ""
	}
}

func createdAt(t time.Time) nodeOpt {
	return func(n *corev1.Node) { n.CreationTimestamp = metav1.NewTime(t) }
}

func condition(t corev1.NodeConditionType, s corev1.ConditionStatus) nodeOpt {
	return func(n *corev1.Node) {
		n.Status.Conditions = append(n.Status.Conditions, corev1.NodeCondition{Type: t, Status: s})
	}
}

func noConditions() nodeOpt {
	return func(n *corev1.Node) { n.Status.Conditions = nil }
}

func unschedulable() nodeOpt {
	return func(n *corev1.Node) { n.Spec.Unschedulable = true }
}

func taint(key, value string, effect corev1.TaintEffect) nodeOpt {
	return func(n *corev1.Node) {
		n.Spec.Taints = append(n.Spec.Taints, corev1.Taint{Key: key, Value: value, Effect: effect})
	}
}

func capacity(rl corev1.ResourceList) nodeOpt {
	return func(n *corev1.Node) { n.Status.Capacity = rl }
}

func allocatable(rl corev1.ResourceList) nodeOpt {
	return func(n *corev1.Node) { n.Status.Allocatable = rl }
}

func Test_listNodes(t *testing.T) {
	created := time.Date(2026, 9, 9, 3, 30, 0, 0, time.UTC)
	client := fake.NewClientset(
		node("master-0", role("control-plane"), createdAt(created)),
		node("worker-0", createdAt(created)),
		node("worker-5", ready(corev1.ConditionFalse), createdAt(created)),
		node("worker-9", ready(corev1.ConditionUnknown), createdAt(created)),
	)

	out, err := listNodes(context.Background(), client)
	require.NoError(t, err)
	require.Len(t, out.Nodes, 4)

	byName := map[string]NodeListItem{}
	for _, n := range out.Nodes {
		byName[n.Name] = n
	}
	require.Equal(t, "Ready", byName["master-0"].Ready)
	require.Equal(t, []string{"control-plane"}, byName["master-0"].Roles)
	require.Empty(t, byName["worker-0"].Roles)
	require.Equal(t, "NotReady", byName["worker-5"].Ready)
	require.Equal(t, "Unknown", byName["worker-9"].Ready)
	require.Equal(t, "v1.37.1", byName["worker-0"].Version)
	require.Equal(t, created, byName["worker-0"].Created)
}

func Test_listNodes_error(t *testing.T) {
	client := fake.NewClientset()
	client.PrependReactor("list", "nodes", kubetest.Forbidden("list", "nodes"))

	_, err := listNodes(context.Background(), client)
	require.Error(t, err)
	require.True(t, apierrors.IsForbidden(err))
}

func Test_getNodeStatus(t *testing.T) {
	created := time.Date(2026, 9, 9, 3, 30, 0, 0, time.UTC)
	client := fake.NewClientset(node("worker-4",
		createdAt(created),
		condition(corev1.NodeMemoryPressure, corev1.ConditionFalse),
		condition(corev1.NodeDiskPressure, corev1.ConditionUnknown),
		taint("gpu", "", corev1.TaintEffectNoSchedule),
		unschedulable(),
	))

	out, err := getNodeStatus(context.Background(), client, "worker-4")
	require.NoError(t, err)
	require.True(t, out.Found)
	require.Equal(t, "Ready", out.Ready)
	require.Equal(t, "v1.37.1", out.Version)
	require.Equal(t, "True", out.Conditions["Ready"])
	require.Equal(t, "False", out.Conditions["MemoryPressure"])
	require.Equal(t, "Unknown", out.Conditions["DiskPressure"])
	require.NotContains(t, out.Conditions, "PIDPressure") // absent stays absent, never manufactured
	require.NotNil(t, out.Schedulable)
	require.False(t, *out.Schedulable)
	require.Equal(t, []string{"gpu:NoSchedule"}, out.Taints)
	require.NotNil(t, out.Created)
	require.Equal(t, created, *out.Created)
}

func Test_getNodeStatus_not_found(t *testing.T) {
	client := fake.NewClientset()

	out, err := getNodeStatus(context.Background(), client, "ghost")
	require.NoError(t, err)
	require.False(t, out.Found)
	require.Equal(t, "ghost", out.Node)
	require.Empty(t, out.Ready)
	require.Nil(t, out.Schedulable)
}

func Test_getNodeStatus_error(t *testing.T) {
	client := fake.NewClientset()
	client.PrependReactor("get", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection refused")
	})

	_, err := getNodeStatus(context.Background(), client, "worker-0")
	require.EqualError(t, err, "connection refused")
}

func Test_readyStatus_absent_condition(t *testing.T) {
	require.Equal(t, "Unknown", readyStatus(node("x", noConditions())))
}

func Test_conditions_and_taints_empty(t *testing.T) {
	require.Nil(t, conditions(node("x", noConditions())))
	require.Nil(t, taints(node("x")))
}

func Test_taints_with_value(t *testing.T) {
	n := node("x", taint("dedicated", "gpu", corev1.TaintEffectNoExecute))
	require.Equal(t, []string{"dedicated=gpu:NoExecute"}, taints(n))
}

func connect(t *testing.T, c clients) *mcp.ClientSession {
	t.Helper()
	return kubetest.Connect(t, func(s *mcp.Server) { register(s, c) })
}

func testClients() clients {
	core := fake.NewClientset(
		node("worker-0", capacity(resourceList("16", "32Gi", "100Gi", "110")),
			allocatable(resourceList("16", "31Gi", "94Gi", "110"))),
		pod("a", "worker-0", corev1.PodRunning),
	)
	metrics := newMetricsFake(nodeMetrics("worker-0", "4", "8Gi"))
	return clients{core: core, metrics: metrics, diskUsage: fixedDisk(10737418240, 107374182400), maxResults: kube.DefaultMaxResults}
}

func Test_node_list_tool(t *testing.T) {
	session := connect(t, testClients())

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "node_list"})
	require.NoError(t, err)
	require.False(t, result.IsError)
	text := result.Content[0].(*mcp.TextContent)
	require.Contains(t, text.Text, "worker-0")
	structured := result.StructuredContent.(map[string]any)
	require.Len(t, structured["nodes"], 1)
}

func Test_node_status_tool(t *testing.T) {
	session := connect(t, testClients())

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "node_status", Arguments: map[string]any{"nodes": []any{"worker-0", "ghost"}},
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	text := result.Content[0].(*mcp.TextContent).Text
	require.Contains(t, text, "[Ready] worker-0")
	require.Contains(t, text, `node "ghost" not found`)
	nodes := kubetest.StructuredList(t, result, "nodes")
	require.Len(t, nodes, 2)
	require.Equal(t, true, nodes[0]["found"])
	require.Equal(t, "ghost", nodes[1]["node"])
	require.Equal(t, false, nodes[1]["found"])
}

func Test_node_resources_tool(t *testing.T) {
	session := connect(t, testClients())

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "node_resources", Arguments: map[string]any{"nodes": []any{"worker-0", "ghost"}},
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	text := result.Content[0].(*mcp.TextContent).Text
	require.Contains(t, text, "usage:")
	require.Contains(t, text, `node "ghost" not found`)
	nodes := kubetest.StructuredList(t, result, "nodes")
	require.Len(t, nodes, 2)
	require.Equal(t, true, nodes[0]["found"])
	require.Equal(t, false, nodes[1]["found"])
}

func Test_node_resources_tool_mixed_disk_access(t *testing.T) {
	c := testClients()
	c.core = fake.NewClientset(node("worker-0"), node("worker-1"))
	c.metrics = newMetricsFake(nodeMetrics("worker-0", "4", "8Gi"), nodeMetrics("worker-1", "2", "4Gi"))
	c.diskUsage = func(_ context.Context, name string) (int64, int64, bool, error) {
		if name == "worker-1" {
			return 0, 0, false, apierrors.NewForbidden(schema.GroupResource{Resource: "nodes/proxy"}, name, errors.New("RBAC denied"))
		}
		return 10737418240, 107374182400, true, nil
	}
	session := connect(t, c)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "node_resources", Arguments: map[string]any{"nodes": []any{"worker-0", "worker-1"}},
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	nodes := kubetest.StructuredList(t, result, "nodes")
	require.Len(t, nodes, 2)
	require.Equal(t, "10737418240", nodes[0]["usage"].(map[string]any)["disk"])
	require.NotContains(t, nodes[0], "usage_note")
	require.NotContains(t, nodes[1]["usage"], "disk")
	require.Equal(t, "2", nodes[1]["usage"].(map[string]any)["cpu"])
	require.Contains(t, nodes[1]["usage_note"], "not permitted")
}

func Test_node_tools_bad_targets(t *testing.T) {
	tests := map[string]struct {
		args   map[string]any
		errMsg string
	}{
		"empty list":  {args: map[string]any{"nodes": []any{}}, errMsg: "nodes must contain at least one target"},
		"blank entry": {args: map[string]any{"nodes": []any{""}}, errMsg: "nodes[0] is empty"},
		"missing":     {args: map[string]any{}, errMsg: "nodes"},
		"string":      {args: map[string]any{"nodes": "worker-0"}, errMsg: "nodes"},
	}
	session := connect(t, testClients())

	for _, tool := range []string{"node_status", "node_resources"} {
		for name, tc := range tests {
			t.Run(tool+"/"+name, func(t *testing.T) {
				result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: tc.args})
				if err != nil {
					require.ErrorContains(t, err, tc.errMsg)
					return
				}
				require.True(t, result.IsError)
				require.Contains(t, result.Content[0].(*mcp.TextContent).Text, tc.errMsg)
			})
		}
	}
}

func Test_Register(t *testing.T) {
	c := kube.Clients{Core: fake.NewClientset(node("worker-0")), Metrics: newMetricsFake()}
	session := kubetest.Connect(t, func(s *mcp.Server) { Register(s, c) })

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "node_status", Arguments: map[string]any{"nodes": []any{"worker-0"}},
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Contains(t, result.Content[0].(*mcp.TextContent).Text, "[Ready] worker-0")
}

func Test_node_list_tool_capped(t *testing.T) {
	c := testClients()
	c.core, c.maxResults = fake.NewClientset(node("worker-0"), node("worker-1")), 1
	session := connect(t, c)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "node_list"})
	require.NoError(t, err)
	kubetest.RequireCapped(t, result, "nodes", 1, 2)
}

func Test_Register_applies_result_limit(t *testing.T) {
	session := kubetest.Connect(t, func(s *mcp.Server) {
		Register(s, kube.Clients{Core: fake.NewClientset(node("worker-0"), node("worker-1")), Metrics: newMetricsFake(), MaxResults: 1})
	})

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "node_list"})
	require.NoError(t, err)
	kubetest.RequireCapped(t, result, "nodes", 1, 2)
}
