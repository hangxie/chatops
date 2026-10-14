// Package pv serves the read-only persistent volume tools: roster with binding and cleaned definition.
package pv

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8s "k8s.io/client-go/kubernetes"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

// PVListItem is one persistent volume's roster entry; Claim is the bound claim as namespace/name.
type PVListItem struct {
	Name          string    `json:"name"`
	Capacity      string    `json:"capacity,omitempty"`
	AccessModes   string    `json:"access_modes,omitempty"`
	ReclaimPolicy string    `json:"reclaim_policy,omitempty"`
	Status        string    `json:"status"`
	Claim         string    `json:"claim,omitempty"`
	StorageClass  string    `json:"storage_class,omitempty"`
	Created       time.Time `json:"created"`
}

// PVListOutput is the structured result of pv_list.
type PVListOutput struct {
	PVs []PVListItem `json:"pvs"`
}

// PVDescribeOutput is the structured result of pv_describe: one entry per requested volume, in request order.
type PVDescribeOutput struct {
	PVs []kube.Described `json:"pvs"`
}

type targetInput struct {
	PVs []string `json:"pvs" jsonschema:"the persistent volume names to describe, at most 5"`
}

// Register adds the persistent volume tools to server.
func Register(server *mcp.Server, c kube.Clients) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "pv_list",
		Description: "List the Kubernetes cluster's persistent volumes with capacity, access modes, reclaim policy, status (Available, Bound, Released, Failed), bound claim, and storage class. Takes no arguments.",
		Annotations: kube.ReadOnly(),
	}, listHandler(c))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "pv_describe",
		Description: "Describe up to 5 Kubernetes persistent volumes in one call: status plus the full object as YAML (capacity, source such as CSI driver and volume handle, node affinity, claim reference). Pass volume names in pvs; a volume that does not exist has found false.",
		Annotations: kube.ReadOnly(),
	}, describeHandler(c))
}

func listHandler(c kube.Clients) mcp.ToolHandlerFor[struct{}, PVListOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, PVListOutput, error) {
		out, err := listPVs(ctx, c.Core)
		if err != nil {
			return nil, PVListOutput{}, err
		}
		return kube.TextResult(formatList(out)), out, nil
	}
}

func describeHandler(c kube.Clients) mcp.ToolHandlerFor[targetInput, PVDescribeOutput] {
	return kube.PerTarget(
		func(in targetInput) ([]string, error) {
			return kube.CappedTargets("pvs", in.PVs, kube.MaxDescribeTargets)
		},
		func(ctx context.Context, name string) (kube.Described, error) { return describe(ctx, c.Core, name) },
		kube.FormatDescribed("persistent volume"),
		func(p []kube.Described) PVDescribeOutput { return PVDescribeOutput{PVs: p} },
	)
}

func listPVs(ctx context.Context, client k8s.Interface) (PVListOutput, error) {
	list, err := client.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return PVListOutput{}, err
	}
	out := PVListOutput{PVs: make([]PVListItem, 0, len(list.Items))}
	for i := range list.Items {
		pv := &list.Items[i]
		item := PVListItem{
			Name: pv.Name, AccessModes: kube.AccessModes(pv.Spec.AccessModes), ReclaimPolicy: string(pv.Spec.PersistentVolumeReclaimPolicy),
			Status: string(pv.Status.Phase), StorageClass: pv.Spec.StorageClassName, Created: pv.CreationTimestamp.Time,
		}
		if q, ok := pv.Spec.Capacity[corev1.ResourceStorage]; ok {
			item.Capacity = q.String()
		}
		if ref := pv.Spec.ClaimRef; ref != nil {
			item.Claim = ref.Namespace + "/" + ref.Name
		}
		out.PVs = append(out.PVs, item)
	}
	return out, nil
}

func describe(ctx context.Context, client k8s.Interface, name string) (kube.Described, error) {
	pv, err := client.CoreV1().PersistentVolumes().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return kube.Described{Name: name}, nil
	}
	if err != nil {
		return kube.Described{}, err
	}
	status := string(pv.Status.Phase)
	pv.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolume"}
	kube.CleanMeta(&pv.ObjectMeta)
	text, err := kube.ToYAML(pv)
	if err != nil {
		return kube.Described{}, err
	}
	return kube.Described{Name: name, Found: true, Health: status, YAML: text}, nil
}

func formatList(out PVListOutput) string {
	if len(out.PVs) == 0 {
		return "no persistent volumes found"
	}
	rows := make([][]string, 0, len(out.PVs))
	for _, p := range out.PVs {
		rows = append(rows, []string{
			p.Name, kube.OrNone(p.Capacity), kube.OrNone(p.AccessModes), kube.OrNone(p.ReclaimPolicy), kube.OrNone(p.Status),
			kube.OrNone(p.Claim), kube.OrNone(p.StorageClass), kube.Age(p.Created),
		})
	}
	return kube.Table([]string{"NAME", "CAPACITY", "ACCESS", "RECLAIM", "STATUS", "CLAIM", "STORAGECLASS", "AGE"}, rows)
}
