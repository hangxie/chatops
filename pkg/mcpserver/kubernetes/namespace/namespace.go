// Package namespace serves the read-only namespace tools.
package namespace

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8s "k8s.io/client-go/kubernetes"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

// NamespaceListItem is one namespace's roster entry; Conditions lists only those that are True, which for a
// namespace always means trouble, such as why it is stuck terminating.
type NamespaceListItem struct {
	Name       string               `json:"name"`
	Status     string               `json:"status"`
	Conditions []NamespaceCondition `json:"conditions,omitempty"`
	Created    time.Time            `json:"created"`
}

// NamespaceCondition is one True condition; Message often names what blocks deletion.
type NamespaceCondition struct {
	Type    string `json:"type"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// NamespaceListOutput is the structured result of namespace_list.
type NamespaceListOutput struct {
	Namespaces []NamespaceListItem `json:"namespaces"`
	// Total counts every match before the cap.
	Total int `json:"total"`
}

func Register(server *mcp.Server, c kube.Clients) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "namespace_list",
		Description: "List the Kubernetes cluster's namespaces with their status (Active or Terminating), age, and any problem conditions, such as why a namespace is stuck terminating. Takes no arguments.",
		Annotations: kube.ReadOnly(),
	}, namespaceListHandler(c))
}

func namespaceListHandler(c kube.Clients) mcp.ToolHandlerFor[struct{}, NamespaceListOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, NamespaceListOutput, error) {
		out, err := listNamespaces(ctx, c.Core)
		if err != nil {
			return nil, NamespaceListOutput{}, err
		}
		out.Namespaces, out.Total = kube.Cap(out.Namespaces, c.ResultLimit())
		return kube.TextResult(formatNamespaceList(out)), out, nil
	}
}

func listNamespaces(ctx context.Context, client k8s.Interface) (NamespaceListOutput, error) {
	list, err := client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return NamespaceListOutput{}, err
	}
	out := NamespaceListOutput{Namespaces: make([]NamespaceListItem, 0, len(list.Items))}
	for i := range list.Items {
		ns := &list.Items[i]
		item := NamespaceListItem{Name: ns.Name, Status: string(ns.Status.Phase), Created: ns.CreationTimestamp.Time}
		for _, c := range ns.Status.Conditions {
			if c.Status == corev1.ConditionTrue {
				item.Conditions = append(item.Conditions, NamespaceCondition{Type: string(c.Type), Reason: c.Reason, Message: c.Message})
			}
		}
		out.Namespaces = append(out.Namespaces, item)
	}
	return out, nil
}
