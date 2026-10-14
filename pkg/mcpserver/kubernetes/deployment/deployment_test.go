package deployment

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

// test fixtures: a deployment builder with functional options, shared across the package's tests.

type deploymentOpt func(*appsv1.Deployment)

// deployment builds a healthy, fully rolled-out deployment selecting app=<name> at revision 1.
func deployment(namespace, name string, opts ...deploymentOpt) *appsv1.Deployment {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace, Name: name, UID: types.UID(namespace + "-" + name), Generation: 1,
			Annotations: map[string]string{revisionAnnotation: "1"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: kubetest.Ptr(int32(2)),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 1, Replicas: 2, UpdatedReplicas: 2, ReadyReplicas: 2, AvailableReplicas: 2,
		},
	}
	for _, o := range opts {
		o(d)
	}
	return d
}

func withStatus(observed int64, replicas, updated, ready, available int32) deploymentOpt {
	return func(d *appsv1.Deployment) {
		d.Status = appsv1.DeploymentStatus{
			ObservedGeneration: observed, Replicas: replicas,
			UpdatedReplicas: updated, ReadyReplicas: ready, AvailableReplicas: available, Conditions: d.Status.Conditions,
		}
	}
}

func withReplicas(n *int32) deploymentOpt { return func(d *appsv1.Deployment) { d.Spec.Replicas = n } }

func paused() deploymentOpt { return func(d *appsv1.Deployment) { d.Spec.Paused = true } }

func stalled() deploymentOpt {
	return func(d *appsv1.Deployment) {
		d.Status.Conditions = append(d.Status.Conditions, appsv1.DeploymentCondition{
			Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded",
		})
	}
}

func Test_deploymentHealth(t *testing.T) {
	tests := map[string]struct {
		d    *appsv1.Deployment
		want string
	}{
		"healthy":                   {d: deployment("ns", "a"), want: healthHealthy},
		"paused wins over stalled":  {d: deployment("ns", "a", paused(), stalled()), want: healthPaused},
		"stalled":                   {d: deployment("ns", "a", stalled(), withStatus(1, 2, 1, 1, 1)), want: healthStalled},
		"scaled to zero":            {d: deployment("ns", "a", withReplicas(kubetest.Ptr(int32(0))), withStatus(1, 0, 0, 0, 0)), want: healthScaledToZero},
		"generation not observed":   {d: deployment("ns", "a", withStatus(0, 2, 2, 2, 2)), want: healthProgressing},
		"updating replicas":         {d: deployment("ns", "a", withStatus(1, 3, 1, 2, 2)), want: healthProgressing},
		"old replicas terminating":  {d: deployment("ns", "a", withStatus(1, 3, 2, 2, 2)), want: healthProgressing},
		"degraded after rollout":    {d: deployment("ns", "a", withStatus(1, 2, 2, 1, 1)), want: healthDegraded},
		"scale-down in progress":    {d: deployment("ns", "a", withStatus(1, 4, 4, 4, 4)), want: healthProgressing},
		"scale-up in progress":      {d: deployment("ns", "a", withReplicas(kubetest.Ptr(int32(4))), withStatus(1, 2, 2, 2, 2)), want: healthProgressing},
		"scale-up awaiting ready":   {d: deployment("ns", "a", withReplicas(kubetest.Ptr(int32(4))), withStatus(1, 4, 4, 2, 2)), want: healthDegraded},
		"overlapping rollouts":      {d: deployment("ns", "a", withReplicas(kubetest.Ptr(int32(3))), withStatus(1, 5, 1, 4, 4)), want: healthProgressing},
		"surge above desired":       {d: deployment("ns", "a", withStatus(1, 3, 2, 3, 3)), want: healthProgressing},
		"unset replicas default 1":  {d: deployment("ns", "a", withReplicas(nil), withStatus(1, 1, 1, 1, 1)), want: healthHealthy},
		"progressing true is fine":  {d: deployment("ns", "a", progressing("NewReplicaSetAvailable")), want: healthHealthy},
		"other false reason is not": {d: deployment("ns", "a", progressingFalse("Other")), want: healthHealthy},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, deploymentHealth(tc.d))
		})
	}
}

func progressing(reason string) deploymentOpt {
	return func(d *appsv1.Deployment) {
		d.Status.Conditions = append(d.Status.Conditions, appsv1.DeploymentCondition{
			Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue, Reason: reason,
		})
	}
}

func progressingFalse(reason string) deploymentOpt {
	return func(d *appsv1.Deployment) {
		d.Status.Conditions = append(d.Status.Conditions, appsv1.DeploymentCondition{
			Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: reason,
		})
	}
}

func Test_listDeployments(t *testing.T) {
	created := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	client := fake.NewClientset(
		kubetest.Namespace("argocd"), kubetest.Namespace("empty"),
		deployment("argocd", "server", func(d *appsv1.Deployment) { d.CreationTimestamp = metav1.NewTime(created) }),
		deployment("argocd", "repo", withStatus(1, 2, 2, 1, 1)),
		deployment("other", "web"),
	)
	tests := map[string]struct {
		namespace string
		want      []string
		errMsg    string
	}{
		"one namespace":     {namespace: "argocd", want: []string{"argocd/repo", "argocd/server"}},
		"trimmed namespace": {namespace: " argocd ", want: []string{"argocd/repo", "argocd/server"}},
		"every namespace":   {namespace: "", want: []string{"argocd/repo", "argocd/server", "other/web"}},
		"empty namespace":   {namespace: "empty", want: []string{}},
		"missing namespace": {namespace: "nope", errMsg: `namespace "nope" does not exist`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			out, err := listDeployments(context.Background(), client, tc.namespace)
			if tc.errMsg != "" {
				require.EqualError(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			got := []string{}
			for _, d := range out.Deployments {
				got = append(got, d.Namespace+"/"+d.Name)
			}
			require.ElementsMatch(t, tc.want, got)
		})
	}

	out, err := listDeployments(context.Background(), client, "argocd")
	require.NoError(t, err)
	require.Equal(t, "argocd", out.Namespace)
	byName := map[string]kube.WorkloadListItem{}
	for _, d := range out.Deployments {
		byName[d.Name] = d
	}
	require.Equal(t, kube.WorkloadListItem{
		Namespace: "argocd", Name: "server", Health: healthHealthy,
		Desired: 2, Ready: 2, Updated: 2, Available: 2, Created: created,
	}, byName["server"])
	require.Equal(t, healthDegraded, byName["repo"].Health)
}

func Test_listDeployments_errors(t *testing.T) {
	tests := map[string]struct {
		verb, resource string
	}{
		"list deployments": {verb: "list", resource: "deployments"},
		"get namespace":    {verb: "get", resource: "namespaces"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			client := fake.NewClientset()
			client.PrependReactor(tc.verb, tc.resource, kubetest.Forbidden(tc.verb, tc.resource))
			_, err := listDeployments(context.Background(), client, "argocd")
			require.ErrorContains(t, err, tc.verb+" denied")
		})
	}
}

func Test_getDeployment(t *testing.T) {
	client := fake.NewClientset(kubetest.Namespace("argocd"), deployment("argocd", "server"))
	tests := map[string]struct {
		ref   kube.ObjectRef
		found bool
		note  string
	}{
		"found":             {ref: kube.ObjectRef{Namespace: "argocd", Name: "server"}, found: true},
		"missing":           {ref: kube.ObjectRef{Namespace: "argocd", Name: "ghost"}},
		"missing namespace": {ref: kube.ObjectRef{Namespace: "nope", Name: "server"}, note: `namespace "nope" does not exist`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			d, note, err := getDeployment(context.Background(), client, tc.ref)
			require.NoError(t, err)
			require.Equal(t, tc.found, d != nil)
			require.Equal(t, tc.note, note)
		})
	}
}

func Test_getDeployment_error(t *testing.T) {
	client := fake.NewClientset()
	client.PrependReactor("get", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("timeout")
	})
	_, _, err := getDeployment(context.Background(), client, kube.ObjectRef{Namespace: "argocd", Name: "server"})
	require.EqualError(t, err, "timeout")
}

func connect(t *testing.T, c kube.Clients) *mcp.ClientSession {
	t.Helper()
	return kubetest.Connect(t, func(s *mcp.Server) { Register(s, c) })
}

func testClients() kube.Clients {
	server := deployment("argocd", "server")
	serverRS := replicaSet(server, "1", "abc")
	return kube.Clients{Core: fake.NewClientset(
		kubetest.Namespace("argocd"), server, serverRS,
		kubetest.WorkloadPod("argocd", "server-1", "server", kubetest.OwnedBy(serverRS)),
	)}
}

func Test_deployment_list_tool(t *testing.T) {
	session := connect(t, testClients())

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "deployment_list", Arguments: map[string]any{"namespace": "argocd"},
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Contains(t, result.Content[0].(*mcp.TextContent).Text, "server")
	require.Len(t, kubetest.StructuredList(t, result, "deployments"), 1)

	result, err = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "deployment_list", Arguments: map[string]any{"namespace": "nope"},
	})
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Contains(t, result.Content[0].(*mcp.TextContent).Text, `namespace "nope" does not exist`)
}

func Test_deployment_pods_tool(t *testing.T) {
	session := connect(t, testClients())

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "deployment_pods", Arguments: map[string]any{"deployments": []any{
			map[string]any{"namespace": "argocd", "name": "server"},
			map[string]any{"namespace": "argocd", "name": "ghost"},
		}},
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	text := result.Content[0].(*mcp.TextContent).Text
	require.Contains(t, text, "server-1")
	require.Contains(t, text, `deployment "argocd/ghost" not found`)
	deployments := kubetest.StructuredList(t, result, "deployments")
	require.Len(t, deployments, 2)
	require.Equal(t, true, deployments[0]["found"])
	require.Len(t, deployments[0]["pods"], 1)
	require.Equal(t, false, deployments[1]["found"])
}

func Test_deployment_describe_tool(t *testing.T) {
	session := connect(t, testClients())

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "deployment_describe", Arguments: map[string]any{"deployments": []any{
			map[string]any{"namespace": "argocd", "name": "server"},
		}},
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Contains(t, result.Content[0].(*mcp.TextContent).Text, "# argocd/server Healthy\napiVersion: apps/v1")
	deployments := kubetest.StructuredList(t, result, "deployments")
	require.Contains(t, deployments[0]["yaml"], "kind: Deployment")
}

func Test_deployment_tools_bad_targets(t *testing.T) {
	refs := func(n int) []any {
		out := make([]any, n)
		for i := range out {
			out[i] = map[string]any{"namespace": "argocd", "name": fmt.Sprintf("d%d", i)}
		}
		return out
	}
	tests := map[string]struct {
		tool   string
		args   map[string]any
		errMsg string
	}{
		"pods empty":        {tool: "deployment_pods", args: map[string]any{"deployments": []any{}}, errMsg: "deployments must contain at least one target"},
		"pods no namespace": {tool: "deployment_pods", args: map[string]any{"deployments": []any{map[string]any{"name": "x"}}}, errMsg: "namespace"},
		"pods blank name":   {tool: "deployment_pods", args: map[string]any{"deployments": []any{map[string]any{"namespace": "argocd", "name": " "}}}, errMsg: "deployments[0] needs both namespace and name"},
		"pods string":       {tool: "deployment_pods", args: map[string]any{"deployments": []any{"argocd/server"}}, errMsg: "deployments"},
		"describe over cap": {tool: "deployment_describe", args: map[string]any{"deployments": refs(kube.MaxDescribeTargets + 1)}, errMsg: "at most 5 are allowed"},
		"pods allows more":  {tool: "deployment_pods", args: map[string]any{"deployments": refs(kube.MaxDescribeTargets + 1)}},
		"describe empty":    {tool: "deployment_describe", args: map[string]any{}, errMsg: "deployments"},
	}
	session := connect(t, testClients())

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args})
			if tc.errMsg == "" {
				require.NoError(t, err)
				require.False(t, result.IsError)
				return
			}
			if err != nil {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.True(t, result.IsError)
			require.Contains(t, result.Content[0].(*mcp.TextContent).Text, tc.errMsg)
		})
	}
}

func Test_deployment_handlers_surface_errors(t *testing.T) {
	core := fake.NewClientset()
	core.PrependReactor("list", "deployments", kubetest.Forbidden("list", "deployments"))
	core.PrependReactor("get", "deployments", kubetest.Forbidden("get", "deployments"))
	c := kube.Clients{Core: core}
	refs := deploymentInput{Deployments: []kube.ObjectRef{{Namespace: "argocd", Name: "server"}}}

	_, _, err := deploymentListHandler(c)(context.Background(), nil, deploymentListInput{})
	require.Error(t, err)
	_, _, err = deploymentPodsHandler(c)(context.Background(), nil, refs)
	require.ErrorContains(t, err, "argocd/server: ")
	_, _, err = deploymentDescribeHandler(c)(context.Background(), nil, refs)
	require.ErrorContains(t, err, "argocd/server: ")
}
