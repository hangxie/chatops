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
	return kube.Table([]string{"NAMESPACE", "NAME", "READY", "STATUS", "RESTARTS", "NODE", "AGE", "OWNER"}, rows)
}

func emptyPodList(out PodListOutput) string {
	var where []string
	if out.Namespace != "" {
		where = append(where, fmt.Sprintf("in namespace %q", out.Namespace))
	}
	if out.Node != "" {
		where = append(where, fmt.Sprintf("on node %q", out.Node))
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
		var used Amounts
		if c.Usage != nil {
			used = *c.Usage
		}
		rows = append(rows, []string{
			c.Container,
			kube.OrNone(kube.HumanCPU(c.Requests.CPU)), kube.OrNone(kube.HumanCPU(c.Limits.CPU)),
			kube.OrNone(kube.WithPercent(kube.HumanCPU(used.CPU), kube.Percent(used.CPU, bound(c.Limits.CPU, c.Requests.CPU)))),
			kube.OrNone(humanMemory(c.Requests.Memory)), kube.OrNone(humanMemory(c.Limits.Memory)),
			kube.OrNone(kube.WithPercent(humanMemory(used.Memory), kube.Percent(used.Memory, bound(c.Limits.Memory, c.Requests.Memory)))),
		})
	}
	text := target + "\n" + kube.Table([]string{"CONTAINER", "CPU REQUEST", "CPU LIMIT", "CPU USED", "MEMORY REQUEST", "MEMORY LIMIT", "MEMORY USED"}, rows)
	if out.UsageNote != "" {
		text += "\nnote: " + out.UsageNote
	}
	return text
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
