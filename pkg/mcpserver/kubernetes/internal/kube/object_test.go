package kube

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

func Test_GetNamespaced(t *testing.T) {
	client := fake.NewClientset(kubetest.Namespace("argocd"),
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "argocd", Name: "cm"}})
	tests := map[string]struct {
		ref    ObjectRef
		found  bool
		note   string
		forbid bool
	}{
		"found":             {ref: ObjectRef{"argocd", "cm"}, found: true},
		"missing":           {ref: ObjectRef{"argocd", "ghost"}},
		"missing namespace": {ref: ObjectRef{"nope", "cm"}, note: `namespace "nope" does not exist`},
		"forbidden":         {ref: ObjectRef{"argocd", "cm"}, forbid: true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			c := client
			if tc.forbid {
				c = fake.NewClientset()
				c.PrependReactor("get", "configmaps", kubetest.Forbidden("get", "configmaps"))
			}
			obj, found, note, err := GetNamespaced(context.Background(), c, tc.ref, c.CoreV1().ConfigMaps(tc.ref.Namespace).Get)
			if tc.forbid {
				require.ErrorContains(t, err, "get denied")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.found, found)
			require.Equal(t, tc.note, note)
			if found {
				require.Equal(t, "cm", obj.Name)
			}
		})
	}
}

func Test_Miss(t *testing.T) {
	require.Equal(t, `pod "argocd/x" not found`, Miss("pod", "argocd/x", ""))
	require.Equal(t, `pod "nope/x" not found: namespace "nope" does not exist`, Miss("pod", "nope/x", `namespace "nope" does not exist`))
}

func Test_FormatDescribed(t *testing.T) {
	format := FormatDescribed("pod")
	tests := map[string]struct {
		d    Described
		want string
	}{
		"namespaced":     {d: Described{Namespace: "argocd", Name: "x", Found: true, Health: "Running", YAML: "kind: Pod\n"}, want: "# argocd/x Running\nkind: Pod"},
		"cluster scoped": {d: Described{Name: "pv-1", Found: true, YAML: "kind: PersistentVolume\n"}, want: "# pv-1\nkind: PersistentVolume"},
		"missing":        {d: Described{Namespace: "argocd", Name: "ghost"}, want: `pod "argocd/ghost" not found`},
		"missing ns":     {d: Described{Namespace: "nope", Name: "x", Note: "gone"}, want: `pod "nope/x" not found: gone`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, format(tc.d))
		})
	}
}
