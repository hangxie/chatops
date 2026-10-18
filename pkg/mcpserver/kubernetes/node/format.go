package node

import (
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

// conditionOrder lists the conditions on-call cares about first; any others follow, sorted.
var conditionOrder = []string{"Ready", "MemoryPressure", "DiskPressure", "PIDPressure", "NetworkUnavailable"}

func formatNodeList(out NodeListOutput) string {
	if len(out.Nodes) == 0 {
		return "no nodes found"
	}
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tSTATUS\tROLES\tAGE\tVERSION")
	for _, n := range out.Nodes {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", n.Name, n.Ready, rolesText(n.Roles), kube.Age(n.Created), n.Version)
	}
	_ = w.Flush()
	return strings.TrimRight(b.String(), "\n") + kube.CapNote(len(out.Nodes), out.Total, "nodes")
}

func formatNodeStatus(out NodeStatus) string {
	if !out.Found {
		return kube.Miss("node", out.Node, "")
	}
	lines := []string{
		fmt.Sprintf("[%s] %s (%s) %s", out.Ready, out.Node, rolesText(out.Roles), out.Version),
		"conditions: " + conditionsText(out.Conditions),
	}
	sched := "schedulable"
	if out.Schedulable != nil && !*out.Schedulable {
		sched = "unschedulable"
	}
	if len(out.Taints) > 0 {
		sched += " taints=" + strings.Join(out.Taints, ",")
	}
	lines = append(lines, "scheduling: "+sched)
	return strings.Join(lines, "\n")
}

func formatNodeResources(out NodeResources) string {
	if !out.Found {
		return kube.Miss("node", out.Node, "")
	}
	lines := []string{out.Node}
	lines = append(lines, "capacity:    "+amountsText(out.Capacity))
	lines = append(lines, "allocatable: "+amountsText(out.Allocatable))
	lines = append(lines, "usage:       "+usageText(out))
	if out.UsageNote != "" {
		lines = append(lines, "note: "+out.UsageNote)
	}
	return strings.Join(lines, "\n")
}

func rolesText(roles []string) string {
	if len(roles) == 0 {
		return "<none>"
	}
	return strings.Join(roles, ",")
}

func conditionsText(m map[string]string) string {
	if len(m) == 0 {
		return "(none)"
	}
	seen := map[string]bool{}
	var parts []string
	for _, k := range conditionOrder {
		if v, ok := m[k]; ok {
			parts = append(parts, k+"="+v)
			seen[k] = true
		}
	}
	var rest []string
	for k := range m {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, " ")
}

// amountsText renders capacity/allocatable, humanizing memory and disk sizes.
func amountsText(a *ResourceAmounts) string {
	if a == nil {
		return "(unknown)"
	}
	return fmt.Sprintf("cpu=%s memory=%s disk=%s pods=%s",
		kube.OrNone(a.CPU), kube.OrNone(humanSize(a.Memory)), kube.OrNone(humanSize(a.Disk)), kube.OrNone(a.Pods))
}

// usageText renders utilization: cpu, memory, and pods as a percentage of allocatable, and disk as a percentage
// of the kubelet filesystem total (DiskTotal), since filesystem used bytes and ephemeral-storage allocatable are
// different accounting boundaries.
func usageText(out NodeResources) string {
	u, alloc := out.Usage, out.Allocatable
	if u == nil {
		return "(unavailable)"
	}
	cpu := kube.OrNone(kube.WithPercent(kube.HumanCPU(u.CPU), kube.Percent(u.CPU, allocField(alloc, "cpu"))))
	mem := kube.OrNone(kube.WithPercent(humanSize(u.Memory), kube.Percent(u.Memory, allocField(alloc, "memory"))))
	disk := kube.OrNone(kube.WithPercent(humanSize(u.Disk), kube.Percent(u.Disk, out.DiskTotal)))
	pods := kube.OrNone(kube.WithPercent(u.Pods, kube.Percent(u.Pods, allocField(alloc, "pods"))))
	return fmt.Sprintf("cpu=%s memory=%s disk=%s pods=%s", cpu, mem, disk, pods)
}

func allocField(a *ResourceAmounts, key string) string {
	if a == nil {
		return ""
	}
	switch key {
	case "cpu":
		return a.CPU
	case "memory":
		return a.Memory
	case "pods":
		return a.Pods
	}
	return ""
}

// humanSize renders a byte-denominated quantity (memory, disk) to GiB; non-byte or empty values pass through.
func humanSize(s string) string {
	if s == "" {
		return ""
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return s
	}
	return fmt.Sprintf("%.1fGi", float64(q.Value())/(1024*1024*1024))
}
