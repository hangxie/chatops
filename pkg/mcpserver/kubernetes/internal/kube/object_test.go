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
	tests := map[string]struct {
		kind, target, note string
		want               string
	}{
		"plain":      {kind: "pod", target: "argocd/x", want: `pod "argocd/x" not found`},
		"note":       {kind: "pod", target: "nope/x", note: `namespace "nope" does not exist`, want: `pod "nope/x" not found: namespace "nope" does not exist`},
		"star":       {kind: "node", target: "worker-*", want: `node "worker-*" not found: ` + wildcardHint},
		"question":   {kind: "pod", target: "argocd/web-?", want: `pod "argocd/web-?" not found: ` + wildcardHint},
		"bracket":    {kind: "node", target: "worker-[0-3]", want: `node "worker-[0-3]" not found: ` + wildcardHint},
		"star+note":  {kind: "pod", target: "nope/*", note: "gone", want: `pod "nope/*" not found: gone; ` + wildcardHint},
		"dash-plain": {kind: "node", target: "worker-0", want: `node "worker-0" not found`},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, Miss(tc.kind, tc.target, tc.note))
		})
	}
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
