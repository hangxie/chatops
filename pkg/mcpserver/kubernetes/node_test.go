package kubernetes

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// test fixtures: a node builder with functional options, shared across the package's tests.

type nodeOpt func(*corev1.Node)

func node(name string, opts ...nodeOpt) *corev1.Node {
	n := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			NodeInfo:   corev1.NodeSystemInfo{KubeletVersion: "v1.37.1"},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
	for _, o := range opts {
		o(n)
	}
	return n
}

func ready(s corev1.ConditionStatus) nodeOpt {
	return func(n *corev1.Node) {
		for i := range n.Status.Conditions {
			if n.Status.Conditions[i].Type == corev1.NodeReady {
				n.Status.Conditions[i].Status = s
				return
			}
		}
	}
}

func role(r string) nodeOpt {
	return func(n *corev1.Node) {
		if n.Labels == nil {
			n.Labels = map[string]string{}
		}
		n.Labels[rolePrefix+r] = ""
	}
}

func createdAt(t time.Time) nodeOpt {
	return func(n *corev1.Node) { n.CreationTimestamp = metav1.NewTime(t) }
}

func condition(t corev1.NodeConditionType, s corev1.ConditionStatus) nodeOpt {
	return func(n *corev1.Node) {
		n.Status.Conditions = append(n.Status.Conditions, corev1.NodeCondition{Type: t, Status: s})
	}
}

func noConditions() nodeOpt {
	return func(n *corev1.Node) { n.Status.Conditions = nil }
}

func unschedulable() nodeOpt {
	return func(n *corev1.Node) { n.Spec.Unschedulable = true }
}

func taint(key, value string, effect corev1.TaintEffect) nodeOpt {
	return func(n *corev1.Node) {
		n.Spec.Taints = append(n.Spec.Taints, corev1.Taint{Key: key, Value: value, Effect: effect})
	}
}

func capacity(rl corev1.ResourceList) nodeOpt {
	return func(n *corev1.Node) { n.Status.Capacity = rl }
}

func allocatable(rl corev1.ResourceList) nodeOpt {
	return func(n *corev1.Node) { n.Status.Allocatable = rl }
}

func forbidden(verb, resource string) k8stesting.ReactionFunc {
	return func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: resource}, "", errors.New(verb+" denied"))
	}
}

func Test_listNodes(t *testing.T) {
	created := time.Date(2026, 9, 9, 3, 30, 0, 0, time.UTC)
	client := fake.NewClientset(
		node("master-0", role("control-plane"), createdAt(created)),
		node("worker-0", createdAt(created)),
		node("worker-5", ready(corev1.ConditionFalse), createdAt(created)),
		node("worker-9", ready(corev1.ConditionUnknown), createdAt(created)),
	)

	out, err := listNodes(context.Background(), client)
	require.NoError(t, err)
	require.Len(t, out.Nodes, 4)

	byName := map[string]NodeListItem{}
	for _, n := range out.Nodes {
		byName[n.Name] = n
	}
	require.Equal(t, "Ready", byName["master-0"].Ready)
	require.Equal(t, []string{"control-plane"}, byName["master-0"].Roles)
	require.Empty(t, byName["worker-0"].Roles)
	require.Equal(t, "NotReady", byName["worker-5"].Ready)
	require.Equal(t, "Unknown", byName["worker-9"].Ready)
	require.Equal(t, "v1.37.1", byName["worker-0"].Version)
	require.Equal(t, created, byName["worker-0"].Created)
}

func Test_listNodes_error(t *testing.T) {
	client := fake.NewClientset()
	client.PrependReactor("list", "nodes", forbidden("list", "nodes"))

	_, err := listNodes(context.Background(), client)
	require.Error(t, err)
	require.True(t, apierrors.IsForbidden(err))
}

func Test_getNodeStatus(t *testing.T) {
	created := time.Date(2026, 9, 9, 3, 30, 0, 0, time.UTC)
	client := fake.NewClientset(node("worker-4",
		createdAt(created),
		condition(corev1.NodeMemoryPressure, corev1.ConditionFalse),
		condition(corev1.NodeDiskPressure, corev1.ConditionUnknown),
		taint("gpu", "", corev1.TaintEffectNoSchedule),
		unschedulable(),
	))

	out, err := getNodeStatus(context.Background(), client, "worker-4")
	require.NoError(t, err)
	require.True(t, out.Found)
	require.Equal(t, "Ready", out.Ready)
	require.Equal(t, "v1.37.1", out.Version)
	require.Equal(t, "True", out.Conditions["Ready"])
	require.Equal(t, "False", out.Conditions["MemoryPressure"])
	require.Equal(t, "Unknown", out.Conditions["DiskPressure"])
	require.NotContains(t, out.Conditions, "PIDPressure") // absent stays absent, never manufactured
	require.NotNil(t, out.Schedulable)
	require.False(t, *out.Schedulable)
	require.Equal(t, []string{"gpu:NoSchedule"}, out.Taints)
	require.NotNil(t, out.Created)
	require.Equal(t, created, *out.Created)
}

func Test_getNodeStatus_not_found(t *testing.T) {
	client := fake.NewClientset()

	out, err := getNodeStatus(context.Background(), client, "ghost")
	require.NoError(t, err)
	require.False(t, out.Found)
	require.Equal(t, "ghost", out.Node)
	require.Empty(t, out.Ready)
	require.Nil(t, out.Schedulable)
}

func Test_getNodeStatus_error(t *testing.T) {
	client := fake.NewClientset()
	client.PrependReactor("get", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection refused")
	})

	_, err := getNodeStatus(context.Background(), client, "worker-0")
	require.EqualError(t, err, "connection refused")
}

func Test_readyStatus_absent_condition(t *testing.T) {
	require.Equal(t, "Unknown", readyStatus(node("x", noConditions())))
}

func Test_conditions_and_taints_empty(t *testing.T) {
	require.Nil(t, conditions(node("x", noConditions())))
	require.Nil(t, taints(node("x")))
}

func Test_taints_with_value(t *testing.T) {
	n := node("x", taint("dedicated", "gpu", corev1.TaintEffectNoExecute))
	require.Equal(t, []string{"dedicated=gpu:NoExecute"}, taints(n))
}
