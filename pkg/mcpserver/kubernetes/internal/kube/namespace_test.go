package kube

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

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

func Test_MissingNamespaces(t *testing.T) {
	names := []string{"argocd", "nope", "default"}
	want := []string{"", `namespace "nope" does not exist`, ""}
	tests := map[string]struct {
		reactor k8stesting.ReactionFunc
		calls   int
		errMsg  string
	}{
		"one list":                 {calls: 1},
		"list forbidden, get each": {reactor: kubetest.Forbidden("list", "namespaces"), calls: 4},
		"list fails":               {reactor: failWith("boom"), errMsg: "boom"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			client := fake.NewClientset(kubetest.Namespace("argocd"), kubetest.Namespace("default"))
			if tc.reactor != nil {
				client.PrependReactor("list", "namespaces", tc.reactor)
			}
			got, err := MissingNamespaces(context.Background(), client, names)
			if tc.errMsg != "" {
				require.EqualError(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Equal(t, want, got)
			require.Len(t, client.Actions(), tc.calls)
		})
	}
}

func failWith(msg string) k8stesting.ReactionFunc {
	return func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New(msg) }
}
