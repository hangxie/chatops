package k8s

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/hangxie/chatops/tool"
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
	tl := &listTool{client: &fakeClient{
		listFn: func(_ context.Context, _, _ string, all bool) (*unstructured.UnstructuredList, *meta.RESTMapping, error) {
			gotAll = all
			return list, podMapping(t), nil
		},
	}}

	res, err := tl.Invoke(context.Background(), tool.Call{Arguments: map[string]string{argKind: "pods", argAllNamespaces: "true"}})
	require.NoError(t, err)
	require.True(t, gotAll)
	require.Contains(t, res.Text, "NAME")
	require.Contains(t, res.Text, "api")
	require.Contains(t, res.Text, "worker")
}

func Test_listTool_Invoke_errors(t *testing.T) {
	testCases := map[string]struct {
		args   map[string]string
		listFn func(context.Context, string, string, bool) (*unstructured.UnstructuredList, *meta.RESTMapping, error)
		errMsg string
	}{
		"missing kind": {args: map[string]string{}, errMsg: "requires a kind"},
		"bad bool":     {args: map[string]string{argKind: "pods", argAllNamespaces: "maybe"}, errMsg: "invalid boolean"},
		"client error": {
			args: map[string]string{argKind: "pods"},
			listFn: func(context.Context, string, string, bool) (*unstructured.UnstructuredList, *meta.RESTMapping, error) {
				return nil, nil, errors.New("boom")
			},
			errMsg: "boom",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			tl := &listTool{client: &fakeClient{listFn: tc.listFn}}
			_, err := tl.Invoke(context.Background(), tool.Call{Arguments: tc.args})
			require.ErrorContains(t, err, tc.errMsg)
		})
	}
}

func Test_listTool_Invoke_cancelledContext(t *testing.T) {
	tl := &listTool{client: &fakeClient{}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := tl.Invoke(ctx, tool.Call{Arguments: map[string]string{argKind: "pods"}})
	require.ErrorIs(t, err, context.Canceled)
}

func Test_parseBool(t *testing.T) {
	testCases := map[string]struct {
		in   string
		want bool
		err  bool
	}{
		"empty": {in: "", want: false},
		"true":  {in: "TRUE", want: true},
		"yes":   {in: "yes", want: true},
		"false": {in: "false", want: false},
		"bad":   {in: "maybe", err: true},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got, err := parseBool(tc.in)
			if tc.err {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
