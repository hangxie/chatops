package k8s

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/hangxie/chatops/tool"
)

// fakeClient is a resourceClient driven by function fields, shared by the get
// and list tool tests.
type fakeClient struct {
	getFn    func(ctx context.Context, kind, namespace, name string) (*unstructured.Unstructured, *meta.RESTMapping, error)
	listFn   func(ctx context.Context, kind, namespace string, all bool) (*unstructured.UnstructuredList, *meta.RESTMapping, error)
	eventsFn func(ctx context.Context, obj *unstructured.Unstructured) []eventInfo
}

func (f *fakeClient) get(ctx context.Context, kind, namespace, name string) (*unstructured.Unstructured, *meta.RESTMapping, error) {
	return f.getFn(ctx, kind, namespace, name)
}

func (f *fakeClient) list(ctx context.Context, kind, namespace string, all bool) (*unstructured.UnstructuredList, *meta.RESTMapping, error) {
	return f.listFn(ctx, kind, namespace, all)
}

func (f *fakeClient) events(ctx context.Context, obj *unstructured.Unstructured) []eventInfo {
	if f.eventsFn == nil {
		return nil
	}
	return f.eventsFn(ctx, obj)
}

func Test_getTool_Invoke(t *testing.T) {
	pod := newPod("web", "api")
	pod.Object["metadata"].(map[string]any)["creationTimestamp"] = "2026-07-23T09:00:00Z"

	client := &fakeClient{
		getFn: func(_ context.Context, _, _, name string) (*unstructured.Unstructured, *meta.RESTMapping, error) {
			p := newPod("web", name)
			return p, nil, nil
		},
		eventsFn: func(context.Context, *unstructured.Unstructured) []eventInfo {
			return []eventInfo{{Type: "Normal", Reason: "Started", Message: "ok"}}
		},
	}
	tl := &getTool{client: client}

	t.Run("brief includes events", func(t *testing.T) {
		res, err := tl.Invoke(context.Background(), tool.Call{Arguments: map[string]string{argKind: "pod", argName: "api"}})
		require.NoError(t, err)
		require.Contains(t, res.Text, "Pod: api")
		require.Contains(t, res.Text, "Started")
	})

	t.Run("json output", func(t *testing.T) {
		res, err := tl.Invoke(context.Background(), tool.Call{Arguments: map[string]string{argKind: "pod", argName: "api", argOutput: "json"}})
		require.NoError(t, err)
		require.Contains(t, res.Text, `"kind": "Pod"`)
	})

	t.Run("multiple names", func(t *testing.T) {
		res, err := tl.Invoke(context.Background(), tool.Call{Arguments: map[string]string{argKind: "pod", argName: "api, worker"}})
		require.NoError(t, err)
		require.Contains(t, res.Text, "Pod: api")
		require.Contains(t, res.Text, "Pod: worker")
	})
}

func Test_getTool_Invoke_errors(t *testing.T) {
	tl := &getTool{client: &fakeClient{
		getFn: func(context.Context, string, string, string) (*unstructured.Unstructured, *meta.RESTMapping, error) {
			return nil, nil, errors.New("boom")
		},
	}}
	testCases := map[string]struct {
		args   map[string]string
		errMsg string
	}{
		"missing kind": {args: map[string]string{argName: "api"}, errMsg: "requires a kind"},
		"missing name": {args: map[string]string{argKind: "pod"}, errMsg: "requires a name"},
		"bad output":   {args: map[string]string{argKind: "pod", argName: "api", argOutput: "toml"}, errMsg: "unknown output"},
		"client error": {args: map[string]string{argKind: "pod", argName: "api"}, errMsg: "boom"},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, err := tl.Invoke(context.Background(), tool.Call{Arguments: tc.args})
			require.ErrorContains(t, err, tc.errMsg)
		})
	}
}

func Test_getTool_Invoke_cancelledContext(t *testing.T) {
	tl := &getTool{client: &fakeClient{}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := tl.Invoke(ctx, tool.Call{Arguments: map[string]string{argKind: "pod", argName: "api"}})
	require.ErrorIs(t, err, context.Canceled)
}

func Test_getTool_Invoke_redactsSecret(t *testing.T) {
	secret := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   map[string]any{"name": "db", "namespace": "web"},
		"data":       map[string]any{"password": "c3VwZXItc2VjcmV0"},
	}}
	tl := &getTool{client: &fakeClient{
		getFn: func(context.Context, string, string, string) (*unstructured.Unstructured, *meta.RESTMapping, error) {
			return secret, nil, nil
		},
	}}
	res, err := tl.Invoke(context.Background(), tool.Call{Arguments: map[string]string{argKind: "secret", argName: "db", argOutput: "yaml"}})
	require.NoError(t, err)
	require.NotContains(t, res.Text, "c3VwZXItc2VjcmV0")
	require.Contains(t, res.Text, redactedValue)
}

func Test_getTool_Invoke_formatError(t *testing.T) {
	// A non-finite float in the fetched object cannot be encoded, surfacing a
	// formatting error from a successful fetch.
	bad := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata":   map[string]any{"name": "api", "namespace": "web"},
		"x":          math.NaN(),
	}}
	tl := &getTool{client: &fakeClient{
		getFn: func(context.Context, string, string, string) (*unstructured.Unstructured, *meta.RESTMapping, error) {
			return bad, nil, nil
		},
	}}
	_, err := tl.Invoke(context.Background(), tool.Call{Arguments: map[string]string{argKind: "pod", argName: "api", argOutput: "json"}})
	require.ErrorContains(t, err, "encode json")
}

func Test_splitNames(t *testing.T) {
	require.Equal(t, []string{"a", "b", "c"}, splitNames("a, b ,c"))
	require.Empty(t, splitNames("  ,, "))
}
