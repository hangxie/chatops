package kubernetes

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

func Test_Register_bad_kubeconfig(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	require.Error(t, Register(server, Config{Kubeconfig: "/no/such/kubeconfig"}))
}

func Test_Register_lists_tools(t *testing.T) {
	// Clients build lazily, so a valid kubeconfig registers the tools without reaching a cluster.
	kubeconfig := kubetest.WriteKubeconfig(t)
	session := kubetest.Connect(t, func(s *mcp.Server) { require.NoError(t, Register(s, Config{Kubeconfig: kubeconfig})) })

	result, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	names := []string{}
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
		require.True(t, tool.Annotations.ReadOnlyHint, tool.Name)
		require.True(t, tool.Annotations.IdempotentHint, tool.Name)
		require.True(t, *tool.Annotations.OpenWorldHint, tool.Name)
		require.NotNil(t, tool.OutputSchema, tool.Name)
	}
	require.ElementsMatch(t, []string{
		"node_list", "node_status", "node_resources", "namespace_list",
		"deployment_list", "deployment_pods", "deployment_describe",
		"pod_list", "pod_describe", "pod_log", "pod_resources",
		"statefulset_list", "statefulset_pods", "statefulset_describe",
		"daemonset_list", "daemonset_pods", "daemonset_describe",
		"replicaset_list", "replicaset_pods", "replicaset_describe",
	}, names)
}
