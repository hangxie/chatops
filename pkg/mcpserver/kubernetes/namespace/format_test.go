package namespace

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

func Test_formatNamespaceList(t *testing.T) {
	created := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	kubetest.FreezeNow(t, &kube.Now, created.Add(34*24*time.Hour))
	out := NamespaceListOutput{Namespaces: []NamespaceListItem{
		{Name: "argocd", Status: "Active", Created: created},
		{Name: "old-app", Status: "Terminating", Created: created, Conditions: []NamespaceCondition{
			{Type: "NamespaceContentRemaining", Reason: "SomeResourcesRemain", Message: "Some resources are remaining: widgets.example.com has 2 resource instances"},
			{Type: "NamespaceFinalizersRemaining", Reason: "SomeFinalizersRemain"},
		}},
	}}

	text := formatNamespaceList(out)
	require.Regexp(t, `NAME\s+STATUS\s+AGE\s+CONDITIONS`, text)
	require.Regexp(t, `argocd\s+Active\s+34d\n`, text)
	require.Regexp(t, `old-app\s+Terminating\s+34d\s+NamespaceContentRemaining,NamespaceFinalizersRemaining\n\n`, text)
	// Only conditions with a message get a detail line, after the table.
	require.True(t, strings.HasSuffix(text, "\n\nold-app: NamespaceContentRemaining: Some resources are remaining: widgets.example.com has 2 resource instances"))

	healthy := formatNamespaceList(NamespaceListOutput{Namespaces: out.Namespaces[:1]})
	require.NotContains(t, healthy, "CONDITIONS")
	require.NotContains(t, healthy, "\n\n")
	require.Regexp(t, `argocd\s+Active\s+34d`, healthy)
}

func Test_formatNamespaceList_empty(t *testing.T) {
	require.Equal(t, "no namespaces found", formatNamespaceList(NamespaceListOutput{}))
	require.Contains(t, formatNamespaceList(NamespaceListOutput{Namespaces: []NamespaceListItem{{Name: "x"}}}), "n/a")
}
