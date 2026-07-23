package k8s

import (
	"context"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/tool"
)

func Test_parseURL(t *testing.T) {
	testCases := map[string]struct {
		raw    string
		want   clusterConfig
		errMsg string
	}{
		"bare":        {raw: "k8s-get://", want: clusterConfig{}},
		"context":     {raw: "k8s-get://?context=prod", want: clusterConfig{context: "prod"}},
		"namespace":   {raw: "k8s-get://?namespace=web", want: clusterConfig{namespace: "web"}},
		"kubeconfig":  {raw: "k8s-get://?kubeconfig=/etc/kube.yaml", want: clusterConfig{kubeconfig: "/etc/kube.yaml"}},
		"host":        {raw: "k8s-get://api.example:6443", errMsg: "takes no host"},
		"unknown-key": {raw: "k8s-get://?cluster=prod", errMsg: `unknown URL parameter "cluster"`},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			u, err := url.Parse(tc.raw)
			require.NoError(t, err)
			cc, err := parseURL(u)
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, cc)
		})
	}
}

func Test_Openers(t *testing.T) {
	path := writeKubeconfig(t)
	values := url.Values{queryKubeconfig: {path}, queryContext: {"alpha"}}

	getURL, err := url.Parse(GetScheme + "://?" + values.Encode())
	require.NoError(t, err)
	getTl, err := GetOpener(context.Background(), getURL, nil)
	require.NoError(t, err)
	require.IsType(t, &getTool{}, getTl)
	require.NoError(t, getTl.Close())

	listURL, err := url.Parse(ListScheme + "://?" + values.Encode())
	require.NoError(t, err)
	listTl, err := ListOpener(context.Background(), listURL, nil)
	require.NoError(t, err)
	require.IsType(t, &listTool{}, listTl)
	require.NoError(t, listTl.Close())
}

func Test_Opener_rejects_bad_url(t *testing.T) {
	for _, opener := range []tool.OpenerFunc{GetOpener, ListOpener} {
		u, err := url.Parse("k8s://host:6443")
		require.NoError(t, err)
		_, err = opener(context.Background(), u, nil)
		require.ErrorContains(t, err, "takes no host")
	}
}

func Test_Descriptors_valid(t *testing.T) {
	require.NoError(t, ListDescriptor.Validate())
	require.NoError(t, GetDescriptor.Validate())
}

func Test_registers_in_tool_registry(t *testing.T) {
	reg := tool.NewRegistry(
		tool.Backend{Scheme: ListScheme, Opener: ListOpener, Descriptor: &ListDescriptor},
		tool.Backend{Scheme: GetScheme, Opener: GetOpener, Descriptor: &GetDescriptor},
	)
	require.Equal(t, []string{GetScheme, ListScheme}, reg.Schemes())
}
