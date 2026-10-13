package kubernetes

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func connect(t *testing.T, c clients) *mcp.ClientSession {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	register(server, c)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "v0"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func testClients() clients {
	core := fake.NewClientset(
		node("worker-0", capacity(resourceList("16", "32Gi", "100Gi", "110")),
			allocatable(resourceList("16", "31Gi", "94Gi", "110"))),
		pod("a", "worker-0", corev1.PodRunning),
	)
	metrics := newMetricsFake(nodeMetrics("worker-0", "4", "8Gi"))
	return clients{core: core, metrics: metrics, diskUsage: fixedDisk(10737418240, 107374182400)}
}

func Test_register_lists_tools(t *testing.T) {
	session := connect(t, testClients())

	result, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)

	names := map[string]*mcp.Tool{}
	for _, tool := range result.Tools {
		names[tool.Name] = tool
	}
	require.Contains(t, names, "node_list")
	require.Contains(t, names, "node_status")
	require.Contains(t, names, "node_resources")

	for _, tool := range names {
		require.True(t, tool.Annotations.ReadOnlyHint, tool.Name)
		require.True(t, tool.Annotations.IdempotentHint, tool.Name)
		require.True(t, *tool.Annotations.OpenWorldHint, tool.Name)
		require.NotNil(t, tool.OutputSchema, tool.Name)
	}
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
	nodes := structuredNodes(t, result)
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
	nodes := structuredNodes(t, result)
	require.Len(t, nodes, 2)
	require.Equal(t, true, nodes[0]["found"])
	require.Equal(t, false, nodes[1]["found"])
}

func Test_node_tools_bad_targets(t *testing.T) {
	tests := map[string]struct {
		args   map[string]any
		errMsg string
	}{
		"empty list":  {args: map[string]any{"nodes": []any{}}, errMsg: "nodes is required"},
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

func structuredNodes(t *testing.T, result *mcp.CallToolResult) []map[string]any {
	t.Helper()
	raw := result.StructuredContent.(map[string]any)["nodes"].([]any)
	nodes := make([]map[string]any, 0, len(raw))
	for _, n := range raw {
		nodes = append(nodes, n.(map[string]any))
	}
	return nodes
}

func Test_Register_bad_kubeconfig(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	require.Error(t, Register(server, Config{Kubeconfig: "/no/such/kubeconfig"}))
}

func Test_Register_success(t *testing.T) {
	// Clients build lazily, so a valid kubeconfig registers the tools without reaching a cluster.
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	require.NoError(t, Register(server, Config{Kubeconfig: writeKubeconfig(t)}))
}
