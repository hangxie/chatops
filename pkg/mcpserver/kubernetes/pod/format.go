package pod

import (
	"fmt"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

func formatPodList(out PodListOutput) string {
	if len(out.Pods) == 0 {
		return emptyPodList(out)
	}
	rows := make([][]string, 0, len(out.Pods))
	for _, p := range out.Pods {
		rows = append(rows, []string{
			p.Namespace, p.Name, fmt.Sprintf("%d/%d", p.Ready, p.Containers), p.Status,
			strconv.Itoa(int(p.Restarts)), kube.OrNone(p.Node), kube.Age(p.Created), kube.OrNone(p.Owner),
		})
	}
	return kube.Table([]string{"NAMESPACE", "NAME", "READY", "STATUS", "RESTARTS", "NODE", "AGE", "OWNER"}, rows) + capNote(len(out.Pods), out.Total, out.Cut)
}

// capNote names each namespace a multi-namespace list shortened, so the model can list it alone.
func capNote(shown, total int, cut []NamespaceCut) string {
	if len(cut) == 0 {
		return kube.CapNote(shown, total, "pods")
	}
	cuts := make([]string, len(cut))
	for i, c := range cut {
		cuts[i] = fmt.Sprintf("%s %d of %d", c.Namespace, c.Shown, c.Total)
	}
	return fmt.Sprintf("\n(showing %d of %d pods, split evenly across namespaces; shortened: %s)", shown, total, strings.Join(cuts, ", "))
}

func emptyPodList(out PodListOutput) string {
	return noPods(out.Namespaces, out.Node)
}

func noPods(namespaces []string, node string) string {
	var where []string
	switch len(namespaces) {
	case 0:
	case 1:
		where = append(where, fmt.Sprintf("in namespace %q", namespaces[0]))
	default:
		quoted := make([]string, len(namespaces))
		for i, namespace := range namespaces {
			quoted[i] = strconv.Quote(namespace)
		}
		where = append(where, "in namespaces "+strings.Join(quoted, ", "))
	}
	if node != "" {
		where = append(where, fmt.Sprintf("on node %q", node))
	}
	if len(where) == 0 {
		return "no pods found"
	}
	return "no pods " + strings.Join(where, " ")
}

func formatPodLog(out PodLog) string {
	target := out.Namespace + "/" + out.Name
	switch {
	case !out.Found:
		return kube.Miss("pod", target, out.Note)
	case out.Note != "":
		return fmt.Sprintf("pod %q: %s", target, out.Note)
	}
	return kube.FormatContainerLogs(target, out.Containers, out.Previous)
}

func formatPodResources(out PodResources) string {
	target := out.Namespace + "/" + out.Name
	if !out.Found {
		return kube.Miss("pod", target, out.Note)
	}
	rows := make([][]string, 0, len(out.Containers))
	for _, c := range out.Containers {
		rows = append(rows, append([]string{c.Container}, resourceCells(c)...))
	}
	text := target + "\n" + kube.Table(append([]string{"CONTAINER"}, resourceHeader...), rows)
	if out.UsageNote != "" {
		text += "\nnote: " + out.UsageNote
	}
	return text
}

// formatScopedResources renders one table for every selected pod, since a block per pod would repeat the header.
func formatScopedResources(out PodResourcesOutput) string {
	if len(out.Pods) == 0 {
		return noPods(out.Namespaces, out.Node)
	}
	var rows [][]string
	var notes []string
	for _, p := range out.Pods {
		for _, c := range p.Containers {
			rows = append(rows, append([]string{p.Namespace, p.Name, c.Container}, resourceCells(c)...))
		}
		if p.UsageNote != "" {
			notes = append(notes, "\nnote: "+p.Namespace+"/"+p.Name+": "+p.UsageNote)
		}
	}
	text := kube.Table(append([]string{"NAMESPACE", "POD", "CONTAINER"}, resourceHeader...), rows)
	return text + strings.Join(notes, "") + capNote(len(out.Pods), out.Total, out.Cut)
}

var resourceHeader = []string{"CPU REQUEST", "CPU LIMIT", "CPU USED", "MEMORY REQUEST", "MEMORY LIMIT", "MEMORY USED"}

// resourceCells renders a container's columns under resourceHeader, with usage as a share of the limit, else the request.
func resourceCells(c ContainerResources) []string {
	var used Amounts
	if c.Usage != nil {
		used = *c.Usage
	}
	return []string{
		kube.OrNone(kube.HumanCPU(c.Requests.CPU)), kube.OrNone(kube.HumanCPU(c.Limits.CPU)),
		kube.OrNone(kube.WithPercent(kube.HumanCPU(used.CPU), kube.Percent(used.CPU, bound(c.Limits.CPU, c.Requests.CPU)))),
		kube.OrNone(humanMemory(c.Requests.Memory)), kube.OrNone(humanMemory(c.Limits.Memory)),
		kube.OrNone(kube.WithPercent(humanMemory(used.Memory), kube.Percent(used.Memory, bound(c.Limits.Memory, c.Requests.Memory)))),
	}
}

// bound is what usage is measured against: the limit, else the request.
func bound(limit, request string) string {
	if limit != "" {
		return limit
	}
	return request
}

// humanMemory renders a memory quantity in MiB, the scale pod requests are usually written in.
func humanMemory(s string) string {
	if s == "" {
		return ""
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return s
	}
	return fmt.Sprintf("%dMi", q.Value()/(1024*1024))
}
