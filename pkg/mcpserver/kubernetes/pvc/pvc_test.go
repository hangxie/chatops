package pvc

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

var created = time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)

func claim(namespace, name string, phase corev1.PersistentVolumeClaimPhase, volume string) *corev1.PersistentVolumeClaim {
	c := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, CreationTimestamp: metav1.NewTime(created), ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "kcm"}}},
		Spec:       corev1.PersistentVolumeClaimSpec{StorageClassName: kubetest.Ptr("ceph-block"), VolumeName: volume},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: phase},
	}
	if phase == corev1.ClaimBound {
		c.Status.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
		c.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")}
	}
	return c
}

func Test_listPVCs(t *testing.T) {
	client := fake.NewClientset(kubetest.Namespace("db"), kubetest.Namespace("empty"),
		claim("db", "data-pg-0", corev1.ClaimBound, "pvc-1"),
		claim("db", "data-pg-1", corev1.ClaimPending, ""),
		claim("kafka", "data-0", corev1.ClaimBound, "pvc-2"),
	)
	tests := map[string]struct {
		namespace string
		want      []string
		errMsg    string
	}{
		"one namespace":     {namespace: " db ", want: []string{"db/data-pg-0", "db/data-pg-1"}},
		"every namespace":   {want: []string{"db/data-pg-0", "db/data-pg-1", "kafka/data-0"}},
		"empty namespace":   {namespace: "empty", want: []string{}},
		"missing namespace": {namespace: "nope", errMsg: `namespace "nope" does not exist`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			out, err := listPVCs(context.Background(), client, tc.namespace)
			if tc.errMsg != "" {
				require.EqualError(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			got := []string{}
			for _, c := range out.PVCs {
				got = append(got, c.Namespace+"/"+c.Name)
			}
			require.ElementsMatch(t, tc.want, got)
		})
	}

	out, err := listPVCs(context.Background(), client, "db")
	require.NoError(t, err)
	require.Contains(t, out.PVCs, PVCListItem{
		Namespace: "db", Name: "data-pg-0", Status: "Bound", Volume: "pvc-1", Capacity: "10Gi",
		AccessModes: "RWO", StorageClass: "ceph-block", Created: created,
	})
	require.Contains(t, out.PVCs, PVCListItem{Namespace: "db", Name: "data-pg-1", Status: "Pending", StorageClass: "ceph-block", Created: created})

	client.PrependReactor("list", "persistentvolumeclaims", kubetest.Forbidden("list", "persistentvolumeclaims"))
	_, err = listPVCs(context.Background(), client, "db")
	require.ErrorContains(t, err, "list denied")
}

func Test_formatList(t *testing.T) {
	kubetest.FreezeNow(t, &kube.Now, created.Add(35*24*time.Hour))
	out := PVCListOutput{PVCs: []PVCListItem{
		{Namespace: "db", Name: "data-pg-0", Status: "Bound", Volume: "pvc-1", Capacity: "10Gi", AccessModes: "RWO", StorageClass: "ceph-block", Created: created},
		{Namespace: "db", Name: "data-pg-1", Status: "Pending", Created: created},
	}}

	text := formatList(out)
	require.Regexp(t, `NAMESPACE\s+NAME\s+STATUS\s+VOLUME\s+CAPACITY\s+ACCESS\s+STORAGECLASS\s+AGE`, text)
	require.Regexp(t, `db\s+data-pg-0\s+Bound\s+pvc-1\s+10Gi\s+RWO\s+ceph-block\s+35d`, text)
	require.Regexp(t, `db\s+data-pg-1\s+Pending\s+n/a\s+n/a\s+n/a\s+n/a\s+35d`, text)
	require.Equal(t, "no persistent volume claims found", formatList(PVCListOutput{}))
	require.Equal(t, `no persistent volume claims in namespace "x"`, formatList(PVCListOutput{Namespace: "x"}))
}

func Test_describe(t *testing.T) {
	client := fake.NewClientset(kubetest.Namespace("db"), claim("db", "data-pg-0", corev1.ClaimBound, "pvc-1"))

	out, err := describe(context.Background(), client, kube.ObjectRef{Namespace: "db", Name: "data-pg-0"})
	require.NoError(t, err)
	require.True(t, out.Found)
	require.Equal(t, "Bound", out.Health)
	require.Contains(t, out.YAML, "apiVersion: v1\nkind: PersistentVolumeClaim\n")
	require.Contains(t, out.YAML, "volumeName: pvc-1")
	require.NotContains(t, out.YAML, "managedFields")

	out, err = describe(context.Background(), client, kube.ObjectRef{Namespace: "nope", Name: "x"})
	require.NoError(t, err)
	require.Equal(t, kube.Described{Namespace: "nope", Name: "x", Note: `namespace "nope" does not exist`}, out)

	client.PrependReactor("get", "persistentvolumeclaims", kubetest.Forbidden("get", "persistentvolumeclaims"))
	_, err = describe(context.Background(), client, kube.ObjectRef{Namespace: "db", Name: "data-pg-0"})
	require.ErrorContains(t, err, "get denied")
}

func Test_tools(t *testing.T) {
	session := kubetest.Connect(t, func(s *mcp.Server) {
		Register(s, kube.Clients{Core: fake.NewClientset(kubetest.Namespace("db"), claim("db", "data-pg-0", corev1.ClaimBound, "pvc-1"))})
	})
	ref := map[string]any{"namespace": "db", "name": "data-pg-0"}
	tests := map[string]struct {
		tool    string
		args    map[string]any
		text    []string
		isError bool
	}{
		"list":            {tool: "pvc_list", args: map[string]any{"namespace": "db"}, text: []string{"data-pg-0", "Bound"}},
		"list missing ns": {tool: "pvc_list", args: map[string]any{"namespace": "nope"}, text: []string{`namespace "nope" does not exist`}, isError: true},
		"describe":        {tool: "pvc_describe", args: map[string]any{"pvcs": []any{ref, map[string]any{"namespace": "db", "name": "ghost"}}}, text: []string{"# db/data-pg-0 Bound\napiVersion: v1", `persistent volume claim "db/ghost" not found`}},
		"describe empty":  {tool: "pvc_describe", args: map[string]any{"pvcs": []any{}}, text: []string{"pvcs must contain at least one target"}, isError: true},
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
				require.NotEmpty(t, kubetest.StructuredList(t, result, "pvcs"))
			}
		})
	}
}
