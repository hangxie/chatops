package kubernetes

import (
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
)

// now is a seam so age rendering is deterministic in tests.
var now = time.Now

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
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", n.Name, n.Ready, rolesText(n.Roles), age(n.Created), n.Version)
	}
	_ = w.Flush()
	return strings.TrimRight(b.String(), "\n")
}

func formatNodeStatus(out NodeStatus) string {
	if !out.Found {
		return fmt.Sprintf("node %q not found", out.Node)
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
		return fmt.Sprintf("node %q not found", out.Node)
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

// formatEach renders one block per target, separated by a blank line.
func formatEach[T any](items []T, format func(T) string) string {
	blocks := make([]string, 0, len(items))
	for _, item := range items {
		blocks = append(blocks, format(item))
	}
	return strings.Join(blocks, "\n\n")
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
		orNone(a.CPU), orNone(humanSize(a.Memory)), orNone(humanSize(a.Disk)), orNone(a.Pods))
}

// usageText renders utilization: cpu, memory, and pods as a percentage of allocatable, and disk as a percentage
// of the kubelet filesystem total (DiskTotal), since filesystem used bytes and ephemeral-storage allocatable are
// different accounting boundaries.
func usageText(out NodeResources) string {
	u, alloc := out.Usage, out.Allocatable
	if u == nil {
		return "(unavailable)"
	}
	cpu := orNone(withPercent(humanCPU(u.CPU), percent(u.CPU, allocField(alloc, "cpu"))))
	mem := orNone(withPercent(humanSize(u.Memory), percent(u.Memory, allocField(alloc, "memory"))))
	disk := orNone(withPercent(humanSize(u.Disk), percent(u.Disk, out.DiskTotal)))
	pods := orNone(withPercent(u.Pods, percent(u.Pods, allocField(alloc, "pods"))))
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

func orNone(s string) string {
	if s == "" {
		return "n/a"
	}
	return s
}

// humanCPU renders a CPU quantity as millicores, matching kubectl (e.g. 276m); empty or unparseable pass through.
func humanCPU(s string) string {
	if s == "" {
		return ""
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return s
	}
	return fmt.Sprintf("%dm", q.MilliValue())
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

func withPercent(value, percent string) string {
	if value == "" || percent == "" {
		return value
	}
	return value + " (" + percent + ")"
}

// percent computes used/total as a whole-percent string; MilliValue keeps fractional CPU correct.
func percent(used, total string) string {
	u, uerr := resource.ParseQuantity(used)
	t, terr := resource.ParseQuantity(total)
	if uerr != nil || terr != nil || t.MilliValue() == 0 {
		return ""
	}
	return fmt.Sprintf("%.0f%%", float64(u.MilliValue())/float64(t.MilliValue())*100)
}

func age(t time.Time) string {
	if t.IsZero() {
		return "<unknown>"
	}
	d := max(now().Sub(t), 0)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
