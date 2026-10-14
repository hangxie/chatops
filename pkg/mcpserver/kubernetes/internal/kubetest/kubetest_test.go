package kubetest

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

func Test_Ptr(t *testing.T) {
	require.Equal(t, 3, *Ptr(3))
}

func Test_FreezeNow(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := time.Now
	t.Run("frozen", func(t *testing.T) {
		FreezeNow(t, &clock, at)
		require.Equal(t, at, clock())
	})
	require.NotEqual(t, at, clock()) // restored after the subtest
}

func Test_Forbidden(t *testing.T) {
	handled, obj, err := Forbidden("get", "pods")(nil)
	require.True(t, handled)
	require.Nil(t, obj)
	require.True(t, apierrors.IsForbidden(err))
	require.ErrorContains(t, err, "get denied")
}

func Test_Namespace(t *testing.T) {
	require.Equal(t, "argocd", Namespace("argocd").Name)
}

type echoOutput struct {
	Items []map[string]any `json:"items"`
}

func Test_Connect_and_StructuredList(t *testing.T) {
	session := Connect(t, func(s *mcp.Server) {
		mcp.AddTool(s, &mcp.Tool{Name: "echo"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, echoOutput, error) {
			return nil, echoOutput{Items: []map[string]any{{"name": "a"}}}, nil
		})
	})

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "echo"})
	require.NoError(t, err)
	require.Equal(t, []map[string]any{{"name": "a"}}, StructuredList(t, result, "items"))
}

type cappedOutput struct {
	Items []map[string]any `json:"items"`
	Total int              `json:"total"`
}

func Test_RequireCapped(t *testing.T) {
	session := Connect(t, func(s *mcp.Server) {
		mcp.AddTool(s, &mcp.Tool{Name: "capped"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, cappedOutput, error) {
			text := &mcp.TextContent{Text: "a\n(showing the first 1 of 3 items)"}
			return &mcp.CallToolResult{Content: []mcp.Content{text}}, cappedOutput{Items: []map[string]any{{"name": "a"}}, Total: 3}, nil
		})
	})

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "capped"})
	require.NoError(t, err)
	RequireCapped(t, result, "items", 1, 3)
}

func Test_RequireCappedTarget(t *testing.T) {
	session := Connect(t, func(s *mcp.Server) {
		mcp.AddTool(s, &mcp.Tool{Name: "capped"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, echoOutput, error) {
			text := &mcp.TextContent{Text: "a\n(showing the first 1 of 2 pods)"}
			entry := map[string]any{"pods": []any{"a"}, "total": 2}
			return &mcp.CallToolResult{Content: []mcp.Content{text}}, echoOutput{Items: []map[string]any{entry}}, nil
		})
	})

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "capped"})
	require.NoError(t, err)
	RequireCappedTarget(t, result, "items", "pods", 1, 2)
}
