package k8s

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// resourceClient is the cluster access the tools depend on; cluster is the
// live implementation and tests substitute a fake.
type resourceClient interface {
	// list returns the objects of one resource type in a namespace, or across
	// all namespaces. It also returns the resolved mapping so callers can tell
	// whether the type is namespaced.
	list(ctx context.Context, kind, namespace string, allNamespaces bool) (*unstructured.UnstructuredList, *meta.RESTMapping, error)

	// get returns one named object of a resource type.
	get(ctx context.Context, kind, namespace, name string) (*unstructured.Unstructured, *meta.RESTMapping, error)

	// events returns the events referencing obj, newest last. It returns nil
	// (no error) when events cannot be listed, so a describe still renders.
	events(ctx context.Context, obj *unstructured.Unstructured) []eventInfo
}

// cluster resolves resource types via discovery and reads objects with the
// dynamic client, so it serves built-in resources and CRDs alike.
type cluster struct {
	dynamic          dynamic.Interface
	mapper           meta.RESTMapper
	defaultNamespace string

	// resetDiscovery invalidates the cached discovery data so a resource type
	// installed after the first discovery becomes visible. It is nil when the
	// mapper is not backed by a refreshable cache (for example in tests).
	resetDiscovery func()
}

// eventInfo is one Kubernetes event, normalized for the describe view.
type eventInfo struct {
	Type    string
	Reason  string
	Message string
	Last    time.Time
}

var eventsGVR = schema.GroupVersionResource{Version: "v1", Resource: "events"}

// mappingFor resolves a user-supplied resource or kind (e.g. "pods", "po",
// "statefulset", "StatefulSet", "deployments.apps") to a REST mapping. The
// resolution order mirrors kubectl so short names, plurals, and kinds all
// work, and CRDs resolve through discovery. A no-match refreshes the cached
// discovery data and retries once, so a CRD installed after the first
// discovery is picked up without restarting the process.
func (c *cluster) mappingFor(resourceOrKind string) (*meta.RESTMapping, error) {
	arg := strings.TrimSpace(resourceOrKind)
	if arg == "" {
		return nil, errors.New("k8s: resource kind is required")
	}

	mapping, err := c.resolveMapping(arg)
	if err != nil && meta.IsNoMatchError(err) && c.resetDiscovery != nil {
		c.resetDiscovery()
		mapping, err = c.resolveMapping(arg)
	}
	if err != nil {
		if meta.IsNoMatchError(err) {
			return nil, fmt.Errorf("k8s: server has no resource type %q", arg)
		}
		return nil, fmt.Errorf("k8s: resolve resource type %q: %w", arg, err)
	}
	return mapping, nil
}

// resolveMapping performs one resolution pass against the current mapper,
// returning the mapper's raw error (including no-match errors) so mappingFor
// can decide whether to refresh discovery and retry.
func (c *cluster) resolveMapping(arg string) (*meta.RESTMapping, error) {
	fullySpecifiedGVR, groupResource := schema.ParseResourceArg(arg)
	gvk := schema.GroupVersionKind{}
	if fullySpecifiedGVR != nil {
		gvk, _ = c.mapper.KindFor(*fullySpecifiedGVR)
	}
	if gvk.Empty() {
		gvk, _ = c.mapper.KindFor(groupResource.WithVersion(""))
	}
	if !gvk.Empty() {
		return c.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	}

	fullySpecifiedGVK, groupKind := schema.ParseKindArg(arg)
	if fullySpecifiedGVK == nil {
		gvk := groupKind.WithVersion("")
		fullySpecifiedGVK = &gvk
	}
	if !fullySpecifiedGVK.Empty() {
		if mapping, err := c.mapper.RESTMapping(fullySpecifiedGVK.GroupKind(), fullySpecifiedGVK.Version); err == nil {
			return mapping, nil
		}
	}

	return c.mapper.RESTMapping(groupKind, gvk.Version)
}

// resourceInterface selects the dynamic client scoped for the mapping: a
// namespaced type is scoped to namespace unless allNamespaces is set.
func (c *cluster) resourceInterface(mapping *meta.RESTMapping, namespace string, allNamespaces bool) dynamic.ResourceInterface {
	if mapping.Scope.Name() != meta.RESTScopeNameNamespace || allNamespaces {
		return c.dynamic.Resource(mapping.Resource)
	}
	return c.dynamic.Resource(mapping.Resource).Namespace(namespace)
}

// namespaceFor resolves the namespace to read a namespaced type from: the
// explicit argument, else the configured default. It returns "" for a
// cluster-scoped type.
func (c *cluster) namespaceFor(mapping *meta.RESTMapping, namespace string) string {
	if mapping.Scope.Name() != meta.RESTScopeNameNamespace {
		return ""
	}
	if namespace != "" {
		return namespace
	}
	if c.defaultNamespace != "" {
		return c.defaultNamespace
	}
	return metav1.NamespaceDefault
}

func (c *cluster) list(ctx context.Context, kind, namespace string, allNamespaces bool) (*unstructured.UnstructuredList, *meta.RESTMapping, error) {
	mapping, err := c.mappingFor(kind)
	if err != nil {
		return nil, nil, err
	}
	ns := c.namespaceFor(mapping, namespace)
	list, err := c.resourceInterface(mapping, ns, allNamespaces).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("k8s: list %s: %w", mapping.Resource.Resource, err)
	}
	return list, mapping, nil
}

func (c *cluster) get(ctx context.Context, kind, namespace, name string) (*unstructured.Unstructured, *meta.RESTMapping, error) {
	mapping, err := c.mappingFor(kind)
	if err != nil {
		return nil, nil, err
	}
	ns := c.namespaceFor(mapping, namespace)
	obj, err := c.resourceInterface(mapping, ns, false).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("k8s: get %s/%s: %w", mapping.Resource.Resource, name, err)
	}
	return obj, mapping, nil
}

// events lists the events referencing obj, matched by involved-object UID and
// returned oldest first. A namespaceless object or any listing error yields
// nil so a describe still renders its object.
func (c *cluster) events(ctx context.Context, obj *unstructured.Unstructured) []eventInfo {
	namespace := obj.GetNamespace()
	if namespace == "" {
		return nil
	}
	selector := "involvedObject.uid=" + string(obj.GetUID())
	list, err := c.dynamic.Resource(eventsGVR).Namespace(namespace).List(ctx, metav1.ListOptions{FieldSelector: selector})
	if err != nil || list == nil {
		return nil
	}
	events := make([]eventInfo, 0, len(list.Items))
	for i := range list.Items {
		events = append(events, eventFrom(&list.Items[i]))
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Last.Before(events[j].Last) })
	return events
}

func eventFrom(obj *unstructured.Unstructured) eventInfo {
	event := eventInfo{}
	event.Type, _, _ = unstructured.NestedString(obj.Object, "type")
	event.Reason, _, _ = unstructured.NestedString(obj.Object, "reason")
	event.Message, _, _ = unstructured.NestedString(obj.Object, "message")
	if ts, found, _ := unstructured.NestedString(obj.Object, "lastTimestamp"); found {
		if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
			event.Last = parsed
		}
	}
	return event
}
