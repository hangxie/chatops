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
		return fmt.Sprintf("namespace %q does not exist", namespace), nil
	}
	if err != nil {
		return "", err
	}
	return "", nil
}
