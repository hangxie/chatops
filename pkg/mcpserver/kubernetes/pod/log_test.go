package pod

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

func Test_getPodLog(t *testing.T) {
	client := fake.NewClientset(kubetest.Namespace("argocd"), kubetest.WorkloadPod("argocd", "server-1", "server"))
	opts := kube.LogOptions{TailLines: 10, Previous: true}
	tests := map[string]struct {
		ref  kube.ObjectRef
		opts kube.LogOptions
		want PodLog
	}{
		"found": {ref: kube.ObjectRef{Namespace: "argocd", Name: "server-1"}, opts: opts, want: PodLog{
			Namespace: "argocd", Name: "server-1", Found: true, Previous: true,
			Containers: []kube.ContainerLog{{Container: "main", Log: "fake logs"}},
		}},
		"unknown container": {ref: kube.ObjectRef{Namespace: "argocd", Name: "server-1"}, opts: kube.LogOptions{Container: "x", TailLines: 10}, want: PodLog{
			Namespace: "argocd", Name: "server-1", Found: true, Note: `container "x" not found (containers: main)`,
		}},
		"missing":           {ref: kube.ObjectRef{Namespace: "argocd", Name: "ghost"}, opts: opts, want: PodLog{Namespace: "argocd", Name: "ghost", Previous: true}},
		"missing namespace": {ref: kube.ObjectRef{Namespace: "nope", Name: "x"}, opts: opts, want: PodLog{Namespace: "nope", Name: "x", Previous: true, Note: `namespace "nope" does not exist`}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			out, err := getPodLog(context.Background(), client, tc.ref, tc.opts)
			require.NoError(t, err)
			require.Equal(t, tc.want, out)
		})
	}
}

func Test_getPodLog_errors(t *testing.T) {
	tests := map[string]k8stesting.ReactionFunc{
		"get pod": kubetest.Forbidden("get", "pods"),
		"read log": func(a k8stesting.Action) (bool, runtime.Object, error) {
			return a.GetSubresource() == "log", nil, errors.New("connection refused")
		},
	}

	for name, reaction := range tests {
		t.Run(name, func(t *testing.T) {
			client := fake.NewClientset(kubetest.WorkloadPod("argocd", "server-1", "server"))
			client.PrependReactor("get", "pods", reaction)
			_, err := getPodLog(context.Background(), client, kube.ObjectRef{Namespace: "argocd", Name: "server-1"}, kube.LogOptions{TailLines: 5})
			require.Error(t, err)
		})
	}
}
