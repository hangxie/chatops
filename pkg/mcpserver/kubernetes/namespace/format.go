package namespace

import (
	"strings"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

// formatNamespaceList adds a CONDITIONS column only when some namespace reports trouble, then one detail line per
// condition message so the table stays one line per namespace.
func formatNamespaceList(out NamespaceListOutput) string {
	if len(out.Namespaces) == 0 {
		return "no namespaces found"
	}
	header := []string{"NAME", "STATUS", "AGE"}
	withConditions := false
	for _, ns := range out.Namespaces {
		withConditions = withConditions || len(ns.Conditions) > 0
	}
	if withConditions {
		header = append(header, "CONDITIONS")
	}
	rows := make([][]string, 0, len(out.Namespaces))
	var details []string
	for _, ns := range out.Namespaces {
		row := []string{ns.Name, kube.OrNone(ns.Status), kube.Age(ns.Created)}
		if withConditions {
			types := make([]string, 0, len(ns.Conditions))
			for _, c := range ns.Conditions {
				types = append(types, c.Type)
				if c.Message != "" {
					details = append(details, ns.Name+": "+c.Type+": "+c.Message)
				}
			}
			row = append(row, strings.Join(types, ","))
		}
		rows = append(rows, row)
	}
	text := kube.Table(header, rows)
	if len(details) > 0 {
		text += "\n\n" + strings.Join(details, "\n")
	}
	return text
}
