package configmap

import (
	"context"
	"strings"
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

var created = time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)

func configMap(namespace, name string, data map[string]string, binary map[string][]byte) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace, Name: name, CreationTimestamp: metav1.NewTime(created),
			ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "helm"}},
		},
		Data:       data,
		BinaryData: binary,
	}
}

func Test_listConfigMaps(t *testing.T) {
	client := fake.NewClientset(kubetest.Namespace("argocd"), kubetest.Namespace("empty"),
		configMap("argocd", "argocd-cm", map[string]string{"url": "https://argo", "timeout": "30s"}, map[string][]byte{"logo": make([]byte, 100)}),
		configMap("argocd", "empty-cm", nil, nil),
		configMap("other", "settings", map[string]string{"a": "b"}, nil),
	)
	tests := map[string]struct {
		namespace string
		want      []string
		errMsg    string
	}{
		"one namespace":     {namespace: "argocd", want: []string{"argocd/argocd-cm", "argocd/empty-cm"}},
		"every namespace":   {want: []string{"argocd/argocd-cm", "argocd/empty-cm", "other/settings"}},
		"empty namespace":   {namespace: "empty", want: []string{}},
		"missing namespace": {namespace: "nope", errMsg: `namespace "nope" does not exist`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			out, err := listConfigMaps(context.Background(), client, tc.namespace)
			if tc.errMsg != "" {
				require.EqualError(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			got := []string{}
			for _, m := range out.ConfigMaps {
				got = append(got, m.Namespace+"/"+m.Name)
			}
			require.ElementsMatch(t, tc.want, got)
		})
	}

	out, err := listConfigMaps(context.Background(), client, "argocd")
	require.NoError(t, err)
	require.Contains(t, out.ConfigMaps, ConfigMapListItem{Namespace: "argocd", Name: "argocd-cm", Keys: 3, Bytes: 115, Created: created})

	client.PrependReactor("list", "configmaps", kubetest.Forbidden("list", "configmaps"))
	_, err = listConfigMaps(context.Background(), client, "argocd")
	require.ErrorContains(t, err, "list denied")
}

func Test_describe(t *testing.T) {
	long := strings.Repeat("x", maxValueBytes+10)
	client := fake.NewClientset(kubetest.Namespace("argocd"),
		configMap("argocd", "argocd-cm", map[string]string{"url": "https://argo", "dashboard.json": long}, map[string][]byte{"logo.png": make([]byte, 300)}))

	out, err := describe(context.Background(), client, kube.ObjectRef{Namespace: "argocd", Name: "argocd-cm"})
	require.NoError(t, err)
	require.True(t, out.Found)
	require.Empty(t, out.Health)
	require.Contains(t, out.YAML, "apiVersion: v1\n")
	require.Contains(t, out.YAML, "kind: ConfigMap\n")
	require.Contains(t, out.YAML, "url: https://argo")
	require.Contains(t, out.YAML, "(cut; 2058 bytes total)")
	require.NotContains(t, out.YAML, long)
	require.Contains(t, out.YAML, "logo.png: <300 bytes of binary data>")
	require.NotContains(t, out.YAML, "managedFields")

	out, err = describe(context.Background(), client, kube.ObjectRef{Namespace: "nope", Name: "x"})
	require.NoError(t, err)
	require.Equal(t, kube.Described{Namespace: "nope", Name: "x", Note: `namespace "nope" does not exist`}, out)

	client.PrependReactor("get", "configmaps", kubetest.Forbidden("get", "configmaps"))
	_, err = describe(context.Background(), client, kube.ObjectRef{Namespace: "argocd", Name: "argocd-cm"})
	require.ErrorContains(t, err, "get denied")
}

func Test_truncate(t *testing.T) {
	require.Equal(t, "short", truncate("short"))
	exact := strings.Repeat("a", maxValueBytes)
	require.Equal(t, exact, truncate(exact))

	// A multibyte character straddling the limit is dropped whole rather than split.
	v := strings.Repeat("a", maxValueBytes-1) + "é" + "tail"
	got := truncate(v)
	require.True(t, strings.HasPrefix(got, strings.Repeat("a", maxValueBytes-1)+"\n… (cut;"))
	require.Contains(t, got, "bytes total)")
}

func Test_formatList(t *testing.T) {
	kubetest.FreezeNow(t, &kube.Now, created.Add(35*24*time.Hour))
	out := ConfigMapListOutput{ConfigMaps: []ConfigMapListItem{
		{Namespace: "argocd", Name: "argocd-cm", Keys: 3, Bytes: 115, Created: created},
		{Namespace: "grafana", Name: "dashboards", Keys: 12, Bytes: 2 * 1024 * 1024, Created: created},
		{Namespace: "grafana", Name: "mid", Keys: 1, Bytes: 1536, Created: created},
	}}

	text := formatList(out)
	require.Regexp(t, `NAMESPACE\s+NAME\s+KEYS\s+SIZE\s+AGE`, text)
	require.Regexp(t, `argocd\s+argocd-cm\s+3\s+115B\s+35d`, text)
	require.Regexp(t, `grafana\s+dashboards\s+12\s+2\.0Mi\s+35d`, text)
	require.Regexp(t, `grafana\s+mid\s+1\s+1\.5Ki\s+35d`, text)
	require.Equal(t, "no configmaps found", formatList(ConfigMapListOutput{}))
	require.Equal(t, `no configmaps in namespace "x"`, formatList(ConfigMapListOutput{Namespace: "x"}))
}

func Test_tools(t *testing.T) {
	session := kubetest.Connect(t, func(s *mcp.Server) {
		Register(s, kube.Clients{Core: fake.NewClientset(kubetest.Namespace("argocd"), configMap("argocd", "argocd-cm", map[string]string{"a": "b"}, nil))})
	})
	ref := map[string]any{"namespace": "argocd", "name": "argocd-cm"}
	tests := map[string]struct {
		tool    string
		args    map[string]any
		text    []string
		isError bool
	}{
		"list":            {tool: "configmap_list", args: map[string]any{"namespace": "argocd"}, text: []string{"argocd-cm"}},
		"list missing ns": {tool: "configmap_list", args: map[string]any{"namespace": "nope"}, text: []string{`namespace "nope" does not exist`}, isError: true},
		"describe":        {tool: "configmap_describe", args: map[string]any{"configmaps": []any{ref, map[string]any{"namespace": "argocd", "name": "ghost"}}}, text: []string{"# argocd/argocd-cm\n", `configmap "argocd/ghost" not found`}},
		"describe empty":  {tool: "configmap_describe", args: map[string]any{"configmaps": []any{}}, text: []string{"configmaps must contain at least one target"}, isError: true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args})
			require.NoError(t, err)
			require.Equal(t, tc.isError, result.IsError)
			for _, want := range tc.text {
				require.Contains(t, result.Content[0].(*mcp.TextContent).Text, want)
			}
			if !tc.isError {
				require.NotEmpty(t, kubetest.StructuredList(t, result, "configmaps"))
			}
		})
	}
}
