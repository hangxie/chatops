package kube

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8s "k8s.io/client-go/kubernetes"
)

// Described is one object's describe entry; detail fields are omitted on a miss.
type Described struct {
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Found     bool   `json:"found"`
	Note      string `json:"note,omitempty"`
	Health    string `json:"health,omitempty"`
	YAML      string `json:"yaml,omitempty"`
}

// GetNamespaced fetches one namespaced object; a miss returns found false, with a note when the namespace itself
// does not exist.
func GetNamespaced[T any](ctx context.Context, client k8s.Interface, ref ObjectRef,
	get func(context.Context, string, metav1.GetOptions) (T, error),
) (obj T, found bool, note string, err error) {
	obj, err = get(ctx, ref.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		note, err = MissingNamespace(ctx, client, ref.Namespace)
		return obj, false, note, err
	}
	return obj, err == nil, "", err
}

// wildcardHint steers a model that passed a pattern back to the list tool, since targets are exact names.
const wildcardHint = "names are exact and wildcards are not supported; list the objects first, then pass the matching names"

// Miss renders a target that does not exist, followed by note and the wildcard hint when they apply.
func Miss(kind, target, note string) string {
	var notes []string
	if note != "" {
		notes = append(notes, note)
	}
	if strings.ContainsAny(target, "*?[") {
		notes = append(notes, wildcardHint)
	}
	text := fmt.Sprintf("%s %q not found", kind, target)
	if len(notes) > 0 {
		text += ": " + strings.Join(notes, "; ")
	}
	return text
}

// FormatDescribed renders describe entries of one kind: a header naming the object and its health, then its YAML.
func FormatDescribed(kind string) func(Described) string {
	return func(d Described) string {
		target := d.Name
		if d.Namespace != "" {
			target = d.Namespace + "/" + d.Name
		}
		if !d.Found {
			return Miss(kind, target, d.Note)
		}
		head := "# " + target
		if d.Health != "" {
			head += " " + d.Health
		}
		return head + "\n" + strings.TrimRight(d.YAML, "\n")
	}
}
