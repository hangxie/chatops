// Package kubetest holds test fixtures shared by the kubernetes resource packages.
package kubetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"
)

// Ptr returns a pointer to v.
func Ptr[T any](v T) *T { return &v }

// FreezeNow pins a clock seam such as kube.Now to at for the rest of the test.
func FreezeNow(t *testing.T, clock *func() time.Time, at time.Time) {
	t.Helper()
	saved := *clock
	*clock = func() time.Time { return at }
	t.Cleanup(func() { *clock = saved })
}

// Forbidden is a fake-clientset reaction that rejects every matching call as RBAC-forbidden.
func Forbidden(verb, resource string) k8stesting.ReactionFunc {
	return func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: resource}, "", errors.New(verb+" denied"))
	}
}

// Namespace builds an existing namespace.
func Namespace(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

// Connect serves the tools register adds over an in-memory transport and returns a connected client session.
func Connect(t *testing.T, register func(*mcp.Server)) *mcp.ClientSession {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	register(server)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "v0"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// StructuredList returns the per-target entries a tool result carries under field.
func StructuredList(t *testing.T, result *mcp.CallToolResult, field string) []map[string]any {
	t.Helper()
	raw := result.StructuredContent.(map[string]any)[field].([]any)
	items := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		items = append(items, item.(map[string]any))
	}
	return items
}
