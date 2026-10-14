package kube

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

func Test_MissingNamespace(t *testing.T) {
	tests := map[string]struct {
		namespace string
		forbid    bool
		want      string
		errMsg    string
	}{
		"exists":    {namespace: "argocd", want: ""},
		"missing":   {namespace: "nope", want: `namespace "nope" does not exist`},
		"forbidden": {namespace: "argocd", forbid: true, errMsg: "get denied"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			client := fake.NewClientset(kubetest.Namespace("argocd"))
			if tc.forbid {
				client.PrependReactor("get", "namespaces", kubetest.Forbidden("get", "namespaces"))
			}
			got, err := MissingNamespace(context.Background(), client, tc.namespace)
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
