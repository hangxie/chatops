package pod

import (
	"context"
	"errors"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	k8s "k8s.io/client-go/kubernetes"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

// scopedPods is the pods a namespaces and node scope selects, shortened to the row cap.
type scopedPods struct {
	namespaces []string
	node       string
	pods       []*corev1.Pod
	// total counts every match before the cap.
	total int
	cut   []NamespaceCut
}

// selectPods selects every namespace when namespaces is empty, and fails naming each requested namespace that does not exist.
func selectPods(ctx context.Context, client k8s.Interface, namespaces []string, node string, limit int) (scopedPods, error) {
	node = strings.TrimSpace(node)
	if len(namespaces) > 0 {
		var err error
		// No cap: max_results bounds the rows; fetchPods makes at most two API calls unless RBAC forces a call per namespace.
		if namespaces, err = kube.CappedTargets("namespaces", namespaces, len(namespaces)); err != nil {
			return scopedPods{}, err
		}
	}
	lists, err := fetchPods(ctx, client, namespaces, node)
	if err != nil {
		return scopedPods{}, err
	}
	out := scopedPods{namespaces: namespaces, node: node}
	var missing []string
	sizes := make([]int, len(lists))
	for i, list := range lists {
		if list.missing != "" {
			missing = append(missing, list.missing)
		}
		sizes[i] = len(list.pods)
		out.total += sizes[i]
	}
	if len(missing) > 0 {
		return scopedPods{}, errors.New(strings.Join(missing, "; "))
	}
	// An even split keeps one large namespace from crowding out the others the caller asked for.
	for i, share := range kube.FairShares(sizes, limit) {
		out.pods = append(out.pods, lists[i].pods[:share]...)
		if share < sizes[i] && len(lists) > 1 {
			out.cut = append(out.cut, NamespaceCut{Namespace: namespaces[i], Shown: share, Total: sizes[i]})
		}
	}
	return out, nil
}

// namespacePods is one namespace's pods, or a note when the namespace does not exist.
type namespacePods struct {
	pods    []*corev1.Pod
	missing string
}

// fetchPods returns one entry per namespace, or one for the whole cluster when namespaces is empty.
func fetchPods(ctx context.Context, client k8s.Interface, namespaces []string, node string) ([]namespacePods, error) {
	if len(namespaces) <= 1 {
		namespace := ""
		if len(namespaces) == 1 {
			namespace = namespaces[0]
		}
		list, err := listNamespacePods(ctx, client, namespace, node)
		return []namespacePods{list}, err
	}
	// One cluster-wide list beats a list per namespace, which the client's rate limit would serialize.
	all, err := rawPods(ctx, client, "", node)
	if apierrors.IsForbidden(err) {
		// Roles granted per namespace still allow a list in each.
		return kube.FetchAll(ctx, namespaces, func(ctx context.Context, namespace string) (namespacePods, error) {
			return listNamespacePods(ctx, client, namespace, node)
		})
	}
	if err != nil {
		return nil, err
	}
	byNamespace := make(map[string][]*corev1.Pod)
	for _, p := range all {
		byNamespace[p.Namespace] = append(byNamespace[p.Namespace], p)
	}
	lists := make([]namespacePods, len(namespaces))
	var empty []int
	for i, namespace := range namespaces {
		if lists[i].pods = byNamespace[namespace]; len(lists[i].pods) == 0 {
			empty = append(empty, i)
		}
	}
	if len(empty) == 0 {
		return lists, nil
	}
	names := make([]string, len(empty))
	for j, i := range empty {
		names[j] = namespaces[i]
	}
	notes, err := kube.MissingNamespaces(ctx, client, names)
	if err != nil {
		return nil, err
	}
	for j, i := range empty {
		lists[i].missing = notes[j]
	}
	return lists, nil
}

func listNamespacePods(ctx context.Context, client k8s.Interface, namespace, node string) (namespacePods, error) {
	pods, err := rawPods(ctx, client, namespace, node)
	if err != nil {
		return namespacePods{}, err
	}
	if len(pods) == 0 && namespace != "" {
		missing, err := kube.MissingNamespace(ctx, client, namespace)
		return namespacePods{missing: missing}, err
	}
	return namespacePods{pods: pods}, nil
}

// rawPods lists one namespace's pods, or every namespace's when namespace is empty.
func rawPods(ctx context.Context, client k8s.Interface, namespace, node string) ([]*corev1.Pod, error) {
	opts := metav1.ListOptions{}
	if node != "" {
		opts.FieldSelector = fields.OneTermEqualSelector("spec.nodeName", node).String()
	}
	list, err := client.CoreV1().Pods(namespace).List(ctx, opts)
	if err != nil {
		return nil, err
	}
	pods := make([]*corev1.Pod, len(list.Items))
	for i := range list.Items {
		pods[i] = &list.Items[i]
	}
	return pods, nil
}
