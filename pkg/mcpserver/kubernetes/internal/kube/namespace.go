package kube

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8s "k8s.io/client-go/kubernetes"
)

// MissingNamespace explains an empty or missing namespaced lookup: it names the namespace when that does not
// exist, and is empty when the namespace exists.
func MissingNamespace(ctx context.Context, client k8s.Interface, namespace string) (string, error) {
	_, err := client.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return NamespaceNotFound(namespace), nil
	}
	if err != nil {
		return "", err
	}
	return "", nil
}

// NamespaceNotFound is the note a tool reports for a requested namespace that does not exist.
func NamespaceNotFound(namespace string) string {
	return fmt.Sprintf("namespace %q does not exist", namespace)
}

// MissingNamespaces checks names with one list, or a get each when listing namespaces is forbidden; notes align with names.
func MissingNamespaces(ctx context.Context, client k8s.Interface, names []string) ([]string, error) {
	list, err := client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if apierrors.IsForbidden(err) {
		return FetchAll(ctx, names, func(ctx context.Context, name string) (string, error) {
			return MissingNamespace(ctx, client, name)
		})
	}
	if err != nil {
		return nil, err
	}
	existing := make(map[string]bool, len(list.Items))
	for _, ns := range list.Items {
		existing[ns.Name] = true
	}
	notes := make([]string, len(names))
	for i, name := range names {
		if !existing[name] {
			notes[i] = NamespaceNotFound(name)
		}
	}
	return notes, nil
}
