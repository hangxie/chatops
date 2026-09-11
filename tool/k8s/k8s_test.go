package k8s

import (
	"context"
	"net/url"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/hangxie/chatops/internal/testutils"
)

func Test_Register_options(t *testing.T) {
	path := writeKubeconfig(t)

	testCases := map[string]struct {
		opts   url.Values
		errMsg string
	}{
		"none":        {opts: nil},
		"kubeconfig":  {opts: url.Values{optionKubeconfig: {path}}},
		"context":     {opts: url.Values{optionKubeconfig: {path}, optionContext: {"alpha"}}},
		"unknown-key": {opts: url.Values{"cluster": {"prod"}}, errMsg: `unknown option cluster`},
		"namespace":   {opts: url.Values{"namespace": {"web"}}, errMsg: `unknown option namespace`},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
			err := Register(srv, nil, tc.opts)
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
		})
	}
}

func Test_Register_bad_kubeconfig(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	err := Register(srv, nil, url.Values{optionKubeconfig: {t.TempDir() + "/missing.yaml"}, optionContext: {"nope"}})
	require.ErrorContains(t, err, "k8s")
}

func Test_RegisterClient_declares_tools(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	require.NoError(t, RegisterClient(srv, &fakeClient{}))
	session := testutils.MCPSession(t, srv)

	require.Equal(t, []string{GetToolName, ListToolName}, testutils.ToolNames(t, session))
}

// Test_tools_typed_arguments checks that the declared schema types
// all-namespaces as a real boolean: the server accepts a JSON boolean and
// rejects a string, so the tools no longer parse booleans out of text.
func Test_tools_typed_arguments(t *testing.T) {
	var gotAll bool
	client := &fakeClient{
		listFn: func(_ context.Context, _, _ string, all bool) (*unstructured.UnstructuredList, *meta.RESTMapping, error) {
			gotAll = all
			return &unstructured.UnstructuredList{}, podMapping(t), nil
		},
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	require.NoError(t, RegisterClient(srv, client))
	session := testutils.MCPSession(t, srv)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      ListToolName,
		Arguments: map[string]any{"kind": "pods", "all-namespaces": true},
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.True(t, gotAll)

	result, err = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      ListToolName,
		Arguments: map[string]any{"kind": "pods", "all-namespaces": "maybe"},
	})
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Contains(t, testutils.ResultText(t, result), "all-namespaces")
}

func Test_get_tool_returns_a_resource(t *testing.T) {
	client := &fakeClient{
		getFn: func(_ context.Context, _, _, name string) (*unstructured.Unstructured, *meta.RESTMapping, error) {
			return newPod("web", name), nil, nil
		},
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	require.NoError(t, RegisterClient(srv, client))
	session := testutils.MCPSession(t, srv)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      GetToolName,
		Arguments: map[string]any{"kind": "pod", "name": "api", "output": "json"},
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Contains(t, testutils.ResultText(t, result), `"kind": "Pod"`)
}

func Test_get_tool_requires_kind_and_name(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	require.NoError(t, RegisterClient(srv, &fakeClient{}))
	session := testutils.MCPSession(t, srv)

	// Both are required properties, so the server rejects the call against
	// the declared schema before the handler runs.
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      GetToolName,
		Arguments: map[string]any{"kind": "pod"},
	})
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Contains(t, testutils.ResultText(t, result), "name")
}
