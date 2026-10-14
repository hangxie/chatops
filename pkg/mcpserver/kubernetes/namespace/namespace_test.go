package namespace

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

func Test_listNamespaces(t *testing.T) {
	created := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	active := kubetest.Namespace("argocd")
	active.Status.Phase = corev1.NamespaceActive
	active.CreationTimestamp = metav1.NewTime(created)
	stuck := kubetest.Namespace("old-app")
	stuck.Status = corev1.NamespaceStatus{Phase: corev1.NamespaceTerminating, Conditions: []corev1.NamespaceCondition{
		{Type: corev1.NamespaceDeletionDiscoveryFailure, Status: corev1.ConditionFalse},
		{
			Type: corev1.NamespaceContentRemaining, Status: corev1.ConditionTrue, Reason: "SomeResourcesRemain",
			Message: "Some resources are remaining: widgets.example.com has 2 resource instances",
		},
		{Type: corev1.NamespaceFinalizersRemaining, Status: corev1.ConditionTrue, Reason: "SomeFinalizersRemain"},
	}}

	out, err := listNamespaces(context.Background(), fake.NewClientset(active, stuck))
	require.NoError(t, err)
	require.Equal(t, []NamespaceListItem{
		{Name: "argocd", Status: "Active", Created: created},
		{Name: "old-app", Status: "Terminating", Conditions: []NamespaceCondition{
			{Type: "NamespaceContentRemaining", Reason: "SomeResourcesRemain", Message: "Some resources are remaining: widgets.example.com has 2 resource instances"},
			{Type: "NamespaceFinalizersRemaining", Reason: "SomeFinalizersRemain"},
		}},
	}, out.Namespaces)
}

func Test_listNamespaces_error(t *testing.T) {
	client := fake.NewClientset()
	client.PrependReactor("list", "namespaces", kubetest.Forbidden("list", "namespaces"))
	_, err := listNamespaces(context.Background(), client)
	require.ErrorContains(t, err, "list denied")
	_, _, err = namespaceListHandler(kube.Clients{Core: client})(context.Background(), nil, struct{}{})
	require.ErrorContains(t, err, "list denied")
}

func connect(t *testing.T, c kube.Clients) *mcp.ClientSession {
	t.Helper()
	return kubetest.Connect(t, func(s *mcp.Server) { Register(s, c) })
}

func Test_namespace_list_tool(t *testing.T) {
	session := connect(t, kube.Clients{Core: fake.NewClientset(kubetest.Namespace("argocd"))})

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "namespace_list"})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Contains(t, result.Content[0].(*mcp.TextContent).Text, "argocd")
	require.Len(t, kubetest.StructuredList(t, result, "namespaces"), 1)
}
