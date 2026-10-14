package deployment

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

func Test_formatDeploymentList(t *testing.T) {
	created := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	kubetest.FreezeNow(t, &kube.Now, created.Add(34*24*time.Hour))
	out := DeploymentListOutput{Deployments: []DeploymentListItem{
		{Namespace: "argocd", Name: "argocd-server", Health: healthHealthy, Desired: 2, Ready: 2, Updated: 2, Available: 2, Created: created},
		{Namespace: "argocd", Name: "argocd-repo", Health: healthStalled, Desired: 2, Ready: 1, Updated: 1, Available: 1, Created: created},
	}}

	text := formatDeploymentList(out)
	require.Regexp(t, `NAMESPACE\s+NAME\s+HEALTH\s+READY\s+UP-TO-DATE\s+AVAILABLE\s+AGE`, text)
	require.Regexp(t, `argocd\s+argocd-server\s+Healthy\s+2/2\s+2\s+2\s+34d`, text)
	require.Regexp(t, `argocd\s+argocd-repo\s+Stalled\s+1/2\s+1\s+1\s+34d`, text)
}

func Test_formatDeploymentList_empty(t *testing.T) {
	require.Equal(t, "no deployments found", formatDeploymentList(DeploymentListOutput{}))
	require.Equal(t, `no deployments in namespace "empty"`, formatDeploymentList(DeploymentListOutput{Namespace: "empty"}))
}

func Test_formatDeploymentPods(t *testing.T) {
	created := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	kubetest.FreezeNow(t, &kube.Now, created.Add(time.Hour))
	tests := map[string]struct {
		out  DeploymentPods
		want []string
	}{
		"pods": {
			out: DeploymentPods{
				Namespace: "argocd", Name: "server", Found: true, Health: healthHealthy, Selector: "app=server",
				Pods: []kube.PodSummary{{Name: "server-1", Ready: 1, Containers: 1, Status: "Running", Node: "worker-0", Created: created, Revision: revisionCurrent}},
			},
			want: []string{"argocd/server Healthy (selector app=server)\n", "server-1", "current"},
		},
		"no pods": {
			out:  DeploymentPods{Namespace: "argocd", Name: "idle", Found: true, Health: healthHealthy, Selector: "app=idle"},
			want: []string{"argocd/idle Healthy (selector app=idle)\nno pods owned by this deployment"},
		},
		"missing": {
			out:  DeploymentPods{Namespace: "argocd", Name: "ghost"},
			want: []string{`deployment "argocd/ghost" not found`},
		},
		"missing namespace": {
			out:  DeploymentPods{Namespace: "nope", Name: "ghost", Note: `namespace "nope" does not exist`},
			want: []string{`deployment "nope/ghost" not found: namespace "nope" does not exist`},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			text := formatDeploymentPods(tc.out)
			for _, w := range tc.want {
				require.Contains(t, text, w)
			}
		})
	}
}
