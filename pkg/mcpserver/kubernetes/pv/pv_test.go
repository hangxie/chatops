package pv

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

func volume(name string, phase corev1.PersistentVolumePhase, claim *corev1.ObjectReference) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(created), ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "csi"}}},
		Spec: corev1.PersistentVolumeSpec{
			Capacity:                      corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
			AccessModes:                   []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
			StorageClassName:              "ceph-block",
			ClaimRef:                      claim,
			PersistentVolumeSource:        corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "rbd.csi.ceph.com", VolumeHandle: "vol-1"}},
		},
		Status: corev1.PersistentVolumeStatus{Phase: phase},
	}
}

func Test_listPVs(t *testing.T) {
	client := fake.NewClientset(
		volume("pvc-1", corev1.VolumeBound, &corev1.ObjectReference{Namespace: "db", Name: "data-pg-0"}),
		volume("pvc-2", corev1.VolumeReleased, nil),
	)

	out, err := listPVs(context.Background(), client)
	require.NoError(t, err)
	require.ElementsMatch(t, []PVListItem{
		{Name: "pvc-1", Capacity: "10Gi", AccessModes: "RWO", ReclaimPolicy: "Delete", Status: "Bound", Claim: "db/data-pg-0", StorageClass: "ceph-block", Created: created},
		{Name: "pvc-2", Capacity: "10Gi", AccessModes: "RWO", ReclaimPolicy: "Delete", Status: "Released", StorageClass: "ceph-block", Created: created},
	}, out.PVs)

	client.PrependReactor("list", "persistentvolumes", kubetest.Forbidden("list", "persistentvolumes"))
	_, err = listPVs(context.Background(), client)
	require.ErrorContains(t, err, "list denied")
}

func Test_formatList(t *testing.T) {
	kubetest.FreezeNow(t, &kube.Now, created.Add(35*24*time.Hour))
	out := PVListOutput{PVs: []PVListItem{
		{Name: "pvc-1", Capacity: "10Gi", AccessModes: "RWO", ReclaimPolicy: "Delete", Status: "Bound", Claim: "db/data-pg-0", StorageClass: "ceph-block", Created: created},
		{Name: "pvc-2", Status: "Available", Created: created},
	}}

	text := formatList(out)
	require.Regexp(t, `NAME\s+CAPACITY\s+ACCESS\s+RECLAIM\s+STATUS\s+CLAIM\s+STORAGECLASS\s+AGE`, text)
	require.Regexp(t, `pvc-1\s+10Gi\s+RWO\s+Delete\s+Bound\s+db/data-pg-0\s+ceph-block\s+35d`, text)
	require.Regexp(t, `pvc-2\s+n/a\s+n/a\s+n/a\s+Available\s+n/a\s+n/a\s+35d`, text)
	require.Equal(t, "no persistent volumes found", formatList(PVListOutput{}))
}

func Test_describe(t *testing.T) {
	client := fake.NewClientset(volume("pvc-1", corev1.VolumeBound, nil))

	out, err := describe(context.Background(), client, "pvc-1")
	require.NoError(t, err)
	require.True(t, out.Found)
	require.Equal(t, "Bound", out.Health)
	require.Contains(t, out.YAML, "apiVersion: v1\nkind: PersistentVolume\n")
	require.Contains(t, out.YAML, "volumeHandle: vol-1")
	require.NotContains(t, out.YAML, "managedFields")

	out, err = describe(context.Background(), client, "ghost")
	require.NoError(t, err)
	require.Equal(t, kube.Described{Name: "ghost"}, out)

	client.PrependReactor("get", "persistentvolumes", kubetest.Forbidden("get", "persistentvolumes"))
	_, err = describe(context.Background(), client, "pvc-1")
	require.ErrorContains(t, err, "get denied")
}

func Test_tools(t *testing.T) {
	session := kubetest.Connect(t, func(s *mcp.Server) {
		Register(s, kube.Clients{Core: fake.NewClientset(volume("pvc-1", corev1.VolumeBound, nil))})
	})
	tests := map[string]struct {
		tool    string
		args    map[string]any
		text    []string
		isError bool
	}{
		"list":              {tool: "pv_list", text: []string{"pvc-1", "Bound"}},
		"describe":          {tool: "pv_describe", args: map[string]any{"pvs": []any{"pvc-1", "ghost"}}, text: []string{"# pvc-1 Bound\napiVersion: v1", `persistent volume "ghost" not found`}},
		"describe empty":    {tool: "pv_describe", args: map[string]any{"pvs": []any{}}, text: []string{"pvs must contain at least one target"}, isError: true},
		"describe over cap": {tool: "pv_describe", args: map[string]any{"pvs": []any{"a", "b", "c", "d", "e", "f"}}, text: []string{"at most 5 are allowed"}, isError: true},
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
				require.NotEmpty(t, kubetest.StructuredList(t, result, "pvs"))
			}
		})
	}
}

func Test_listHandler_error(t *testing.T) {
	client := fake.NewClientset()
	client.PrependReactor("list", "persistentvolumes", kubetest.Forbidden("list", "persistentvolumes"))
	_, _, err := listHandler(kube.Clients{Core: client})(context.Background(), nil, struct{}{})
	require.ErrorContains(t, err, "list denied")
}
