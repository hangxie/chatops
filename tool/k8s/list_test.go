package k8s

import (
	"context"
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func podMapping(t *testing.T) *meta.RESTMapping {
	t.Helper()
	mapping, err := testMapper().RESTMapping(schema.GroupKind{Kind: "Pod"}, "v1")
	require.NoError(t, err)
	return mapping
}

func Test_listTool_Invoke(t *testing.T) {
	list := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*newPod("web", "api"), *newPod("web", "worker")}}
	var gotAll bool
	client := &fakeClient{
		listFn: func(_ context.Context, _, _ string, all bool) (*unstructured.UnstructuredList, *meta.RESTMapping, error) {
			gotAll = all
			return list, podMapping(t), nil
		},
	}

	res, _, err := listResources(context.Background(), client, ListArgs{Kind: "pods", AllNamespaces: true})
	require.NoError(t, err)
	require.True(t, gotAll)
	text := res.Content[0].(*mcp.TextContent).Text
	require.Contains(t, text, "NAME")
	require.Contains(t, text, "api")
	require.Contains(t, text, "worker")
}

func Test_listTool_Invoke_errors(t *testing.T) {
	testCases := map[string]struct {
		args   ListArgs
		listFn func(context.Context, string, string, bool) (*unstructured.UnstructuredList, *meta.RESTMapping, error)
		errMsg string
	}{
		"missing kind": {args: ListArgs{}, errMsg: "requires a kind"},
		"client error": {
			args: ListArgs{Kind: "pods"},
			listFn: func(context.Context, string, string, bool) (*unstructured.UnstructuredList, *meta.RESTMapping, error) {
				return nil, nil, errors.New("boom")
			},
			errMsg: "boom",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, _, err := listResources(context.Background(), &fakeClient{listFn: tc.listFn}, tc.args)
			require.ErrorContains(t, err, tc.errMsg)
		})
	}
}

func Test_listTool_Invoke_cancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := listResources(ctx, &fakeClient{}, ListArgs{Kind: "pods"})
	require.ErrorIs(t, err, context.Canceled)
}
