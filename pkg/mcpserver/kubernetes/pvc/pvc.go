// Package pvc serves the read-only persistent volume claim tools: roster with binding and cleaned definition.
package pvc

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8s "k8s.io/client-go/kubernetes"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

// PVCListItem is one claim's roster entry; Capacity is what the bound volume provides.
type PVCListItem struct {
	Namespace    string    `json:"namespace"`
	Name         string    `json:"name"`
	Status       string    `json:"status"`
	Volume       string    `json:"volume,omitempty"`
	Capacity     string    `json:"capacity,omitempty"`
	AccessModes  string    `json:"access_modes,omitempty"`
	StorageClass string    `json:"storage_class,omitempty"`
	Created      time.Time `json:"created"`
}

// PVCListOutput is the structured result of pvc_list; Namespace is empty when listing all.
type PVCListOutput struct {
	Namespace string        `json:"namespace,omitempty"`
	PVCs      []PVCListItem `json:"pvcs"`
}

// PVCDescribeOutput is the structured result of pvc_describe: one entry per requested claim, in request order.
type PVCDescribeOutput struct {
	PVCs []kube.Described `json:"pvcs"`
}

type listInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"the namespace to list; omit to list every namespace"`
}

type targetInput struct {
	PVCs []kube.ObjectRef `json:"pvcs" jsonschema:"the persistent volume claims to describe, at most 5"`
}

// Register adds the persistent volume claim tools to server.
func Register(server *mcp.Server, c kube.Clients) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "pvc_list",
		Description: "List Kubernetes persistent volume claims with status (Bound, Pending, Lost), bound volume, capacity, access modes, and storage class. Pass namespace to list one namespace; omit it to list every namespace.",
		Annotations: kube.ReadOnly(),
	}, listHandler(c))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "pvc_describe",
		Description: "Describe up to 5 Kubernetes persistent volume claims in one call: status plus the full object as YAML (requested size, storage class, volume, conditions such as a pending resize). Pass claims as {namespace, name}; a claim that does not exist has found false.",
		Annotations: kube.ReadOnly(),
	}, describeHandler(c))
}

func listHandler(c kube.Clients) mcp.ToolHandlerFor[listInput, PVCListOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in listInput) (*mcp.CallToolResult, PVCListOutput, error) {
		out, err := listPVCs(ctx, c.Core, in.Namespace)
		if err != nil {
			return nil, PVCListOutput{}, err
		}
		return kube.TextResult(formatList(out)), out, nil
	}
}

func describeHandler(c kube.Clients) mcp.ToolHandlerFor[targetInput, PVCDescribeOutput] {
	return kube.PerTarget(
		func(in targetInput) ([]kube.ObjectRef, error) {
			return kube.ObjectTargets("pvcs", in.PVCs, kube.MaxDescribeTargets)
		},
		func(ctx context.Context, ref kube.ObjectRef) (kube.Described, error) {
			return describe(ctx, c.Core, ref)
		},
		kube.FormatDescribed("persistent volume claim"),
		func(p []kube.Described) PVCDescribeOutput { return PVCDescribeOutput{PVCs: p} },
	)
}

func listPVCs(ctx context.Context, client k8s.Interface, namespace string) (PVCListOutput, error) {
	namespace = strings.TrimSpace(namespace)
	list, err := client.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return PVCListOutput{}, err
	}
	if err := kube.CheckNamespace(ctx, client, namespace, len(list.Items)); err != nil {
		return PVCListOutput{}, err
	}
	out := PVCListOutput{Namespace: namespace, PVCs: make([]PVCListItem, 0, len(list.Items))}
	for i := range list.Items {
		c := &list.Items[i]
		item := PVCListItem{
			Namespace: c.Namespace, Name: c.Name, Status: string(c.Status.Phase), Volume: c.Spec.VolumeName,
			AccessModes: kube.AccessModes(c.Status.AccessModes), Created: c.CreationTimestamp.Time,
		}
		if q, ok := c.Status.Capacity[corev1.ResourceStorage]; ok {
			item.Capacity = q.String()
		}
		if c.Spec.StorageClassName != nil {
			item.StorageClass = *c.Spec.StorageClassName
		}
		out.PVCs = append(out.PVCs, item)
	}
	return out, nil
}

func describe(ctx context.Context, client k8s.Interface, ref kube.ObjectRef) (kube.Described, error) {
	c, found, note, err := kube.GetNamespaced(ctx, client, ref, client.CoreV1().PersistentVolumeClaims(ref.Namespace).Get)
	if !found {
		return kube.Described{Namespace: ref.Namespace, Name: ref.Name, Note: note}, err
	}
	status := string(c.Status.Phase)
	c.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"}
	kube.CleanMeta(&c.ObjectMeta)
	text, err := kube.ToYAML(c)
	if err != nil {
		return kube.Described{}, err
	}
	return kube.Described{Namespace: ref.Namespace, Name: ref.Name, Found: true, Health: status, YAML: text}, nil
}

func formatList(out PVCListOutput) string {
	if len(out.PVCs) == 0 {
		if out.Namespace != "" {
			return fmt.Sprintf("no persistent volume claims in namespace %q", out.Namespace)
		}
		return "no persistent volume claims found"
	}
	rows := make([][]string, 0, len(out.PVCs))
	for _, c := range out.PVCs {
		rows = append(rows, []string{
			c.Namespace, c.Name, kube.OrNone(c.Status), kube.OrNone(c.Volume), kube.OrNone(c.Capacity),
			kube.OrNone(c.AccessModes), kube.OrNone(c.StorageClass), kube.Age(c.Created),
		})
	}
	return kube.Table([]string{"NAMESPACE", "NAME", "STATUS", "VOLUME", "CAPACITY", "ACCESS", "STORAGECLASS", "AGE"}, rows)
}
