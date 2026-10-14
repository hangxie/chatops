package deployment

import (
	"fmt"
	"strconv"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

func formatDeploymentList(out DeploymentListOutput) string {
	if len(out.Deployments) == 0 {
		if out.Namespace != "" {
			return fmt.Sprintf("no deployments in namespace %q", out.Namespace)
		}
		return "no deployments found"
	}
	rows := make([][]string, 0, len(out.Deployments))
	for _, d := range out.Deployments {
		rows = append(rows, []string{
			d.Namespace, d.Name, d.Health, fmt.Sprintf("%d/%d", d.Ready, d.Desired),
			strconv.Itoa(int(d.Updated)), strconv.Itoa(int(d.Available)), kube.Age(d.Created),
		})
	}
	return kube.Table([]string{"NAMESPACE", "NAME", "HEALTH", "READY", "UP-TO-DATE", "AVAILABLE", "AGE"}, rows)
}

func formatDeploymentPods(out DeploymentPods) string {
	if !out.Found {
		return kube.Miss("deployment", out.Namespace+"/"+out.Name, out.Note)
	}
	head := fmt.Sprintf("%s/%s %s (selector %s)", out.Namespace, out.Name, out.Health, out.Selector)
	if len(out.Pods) == 0 {
		return head + "\nno pods owned by this deployment"
	}
	return head + "\n" + kube.FormatPodTable(out.Pods)
}
