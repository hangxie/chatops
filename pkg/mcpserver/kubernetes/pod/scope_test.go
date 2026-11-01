package pod

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

// Test_selectPods_namespaced_grants covers callers whose grants forbid the cluster-wide pod list, the namespace list, or both.
func Test_selectPods_namespaced_grants(t *testing.T) {
	tests := map[string]struct {
		forbidPods, forbidNamespaces bool
	}{
		"pods and namespaces": {forbidPods: true, forbidNamespaces: true},
		"namespaces only":     {forbidNamespaces: true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			client := fake.NewClientset(kubetest.Namespace("argocd"), kubetest.Namespace("empty"),
				kubetest.WorkloadPod("argocd", "server-1", "server"), kubetest.WorkloadPod("other", "web-1", "web"))
			if tc.forbidPods {
				client.PrependReactor("list", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
					if a.GetNamespace() == "" {
						return kubetest.Forbidden("list", "pods")(a)
					}
					return false, nil, nil
				})
			}
			if tc.forbidNamespaces {
				client.PrependReactor("list", "namespaces", kubetest.Forbidden("list", "namespaces"))
			}

			out, err := selectPods(context.Background(), client, []string{"argocd", "other", "empty"}, "", kube.DefaultMaxResults)
			require.NoError(t, err)
			require.Equal(t, 2, out.total)
			_, err = selectPods(context.Background(), client, []string{"argocd", "nope"}, "", kube.DefaultMaxResults)
			require.EqualError(t, err, `namespace "nope" does not exist`)
		})
	}
}

func Test_selectPods_api_calls(t *testing.T) {
	var objects []runtime.Object
	namespaces := make([]string, 40)
	for i := range namespaces {
		namespaces[i] = fmt.Sprintf("ns-%d", i)
		objects = append(objects, kubetest.Namespace(namespaces[i]), kubetest.WorkloadPod(namespaces[i], "p", "p"))
	}
	objects = append(objects, kubetest.Namespace("empty"))
	tests := map[string]struct {
		namespaces []string
		want       int
		errMsg     string
	}{
		"every namespace has pods": {namespaces: namespaces, want: 1},
		"one existing but empty":   {namespaces: append([]string{"empty"}, namespaces...), want: 2},
		"one missing":              {namespaces: append([]string{"nope"}, namespaces...), want: 2, errMsg: `namespace "nope" does not exist`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			client := fake.NewClientset(objects...)
			_, err := selectPods(context.Background(), client, tc.namespaces, "", kube.DefaultMaxResults)
			if tc.errMsg != "" {
				require.EqualError(t, err, tc.errMsg)
			} else {
				require.NoError(t, err)
			}
			require.Len(t, client.Actions(), tc.want)
		})
	}
}
