// Package service serves the read-only service tools: health from endpoints, backing pods, and cleaned definition.
package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	k8s "k8s.io/client-go/kubernetes"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

// Health words; see docs/mcp-k8s.md for the rules.
const (
	healthExternal    = "External"
	healthNoEndpoints = "NoEndpoints"
	healthUnavailable = "Unavailable"
	healthDegraded    = "Degraded"
	healthHealthy     = "Healthy"
)

// ServiceListItem is one service's roster entry; Ready and Endpoints count its non-terminating endpoints.
type ServiceListItem struct {
	Namespace  string    `json:"namespace"`
	Name       string    `json:"name"`
	Type       string    `json:"type"`
	ClusterIP  string    `json:"cluster_ip,omitempty"`
	ExternalIP string    `json:"external_ip,omitempty"`
	Ports      string    `json:"ports,omitempty"`
	Health     string    `json:"health"`
	Ready      int       `json:"ready"`
	Endpoints  int       `json:"endpoints"`
	Created    time.Time `json:"created"`
}

// ServiceListOutput is the structured result of service_list; Namespace is empty when listing all.
type ServiceListOutput struct {
	Namespace string            `json:"namespace,omitempty"`
	Services  []ServiceListItem `json:"services"`
}

type listInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"the namespace to list; omit to list every namespace"`
}

type targetInput struct {
	Services []kube.ObjectRef `json:"services" jsonschema:"the services to report on, all in one call"`
}

// Register adds the service tools to server.
func Register(server *mcp.Server, c kube.Clients) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "service_list",
		Description: "List Kubernetes services with type, cluster IP, external IP, ports, ready over total endpoints, and health (Healthy, Degraded, Unavailable, NoEndpoints, External). Pass namespace to list one namespace; omit it to list every namespace.",
		Annotations: kube.ReadOnly(),
	}, listHandler(c))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "service_pods",
		Description: "List the pods actually behind one or more Kubernetes services in one call, from their endpoints: pod, address, whether it is ready, serving, or terminating, and node. Pass every service of interest in services as {namespace, name}; a service that does not exist has found false.",
		Annotations: kube.ReadOnly(),
	}, podsHandler(c))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "service_describe",
		Description: "Describe up to 5 Kubernetes services in one call: health plus the full service object as YAML (type, selector, ports, IPs, session affinity, status). Pass services as {namespace, name}; a service that does not exist has found false.",
		Annotations: kube.ReadOnly(),
	}, describeHandler(c))
}

func listHandler(c kube.Clients) mcp.ToolHandlerFor[listInput, ServiceListOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in listInput) (*mcp.CallToolResult, ServiceListOutput, error) {
		out, err := listServices(ctx, c.Core, in.Namespace)
		if err != nil {
			return nil, ServiceListOutput{}, err
		}
		return kube.TextResult(formatList(out)), out, nil
	}
}

func podsHandler(c kube.Clients) mcp.ToolHandlerFor[targetInput, ServicePodsOutput] {
	return kube.PerTarget(
		func(in targetInput) ([]kube.ObjectRef, error) {
			return kube.ObjectTargets("services", in.Services, kube.MaxTargets)
		},
		func(ctx context.Context, ref kube.ObjectRef) (ServicePods, error) { return getPods(ctx, c.Core, ref) },
		formatPods,
		func(s []ServicePods) ServicePodsOutput { return ServicePodsOutput{Services: s} },
	)
}

func describeHandler(c kube.Clients) mcp.ToolHandlerFor[targetInput, ServiceDescribeOutput] {
	return kube.PerTarget(
		func(in targetInput) ([]kube.ObjectRef, error) {
			return kube.ObjectTargets("services", in.Services, kube.MaxDescribeTargets)
		},
		func(ctx context.Context, ref kube.ObjectRef) (kube.Described, error) {
			return describe(ctx, c.Core, ref)
		},
		kube.FormatDescribed("service"),
		func(s []kube.Described) ServiceDescribeOutput { return ServiceDescribeOutput{Services: s} },
	)
}

// listServices counts endpoints with one EndpointSlice list for the whole scope rather than one per service.
func listServices(ctx context.Context, client k8s.Interface, namespace string) (ServiceListOutput, error) {
	namespace = strings.TrimSpace(namespace)
	list, err := client.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return ServiceListOutput{}, err
	}
	if err := kube.CheckNamespace(ctx, client, namespace, len(list.Items)); err != nil {
		return ServiceListOutput{}, err
	}
	slices, err := client.DiscoveryV1().EndpointSlices(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return ServiceListOutput{}, err
	}
	bySvc := map[string][]discoveryv1.EndpointSlice{}
	for _, s := range slices.Items {
		key := s.Namespace + "/" + s.Labels[discoveryv1.LabelServiceName]
		bySvc[key] = append(bySvc[key], s)
	}
	out := ServiceListOutput{Namespace: namespace, Services: make([]ServiceListItem, 0, len(list.Items))}
	for i := range list.Items {
		svc := &list.Items[i]
		ready, total := countEndpoints(mergeEndpoints(bySvc[svc.Namespace+"/"+svc.Name]))
		out.Services = append(out.Services, ServiceListItem{
			Namespace: svc.Namespace, Name: svc.Name, Type: string(svc.Spec.Type), ClusterIP: svc.Spec.ClusterIP,
			ExternalIP: externalIP(svc), Ports: ports(svc), Health: health(svc, ready, total), Ready: ready,
			Endpoints: total, Created: svc.CreationTimestamp.Time,
		})
	}
	return out, nil
}

// mergeEndpoints combines a backend's entries across slices, one per address family on a dual-stack service, and
// stays conservative when they disagree: ready and serving only if every entry says so, terminating if any does.
func mergeEndpoints(slices []discoveryv1.EndpointSlice) []Endpoint {
	index := map[string]int{}
	var out []Endpoint
	for _, s := range slices {
		for _, e := range s.Endpoints {
			ep, key := endpoint(e), endpointKey(e)
			i, seen := index[key]
			if !seen {
				index[key] = len(out)
				out = append(out, ep)
				continue
			}
			m := &out[i]
			m.Addresses = append(m.Addresses, ep.Addresses...)
			m.Ready, m.Serving = m.Ready && ep.Ready, m.Serving && ep.Serving
			m.Terminating = m.Terminating || ep.Terminating
			if m.Node == "" {
				m.Node = ep.Node
			}
		}
	}
	return out
}

// countEndpoints counts the non-terminating backends and how many of them are ready.
func countEndpoints(endpoints []Endpoint) (ready, total int) {
	for _, e := range endpoints {
		if e.Terminating {
			continue
		}
		total++
		if e.Ready {
			ready++
		}
	}
	return ready, total
}

func endpointKey(e discoveryv1.Endpoint) string {
	if e.TargetRef != nil && e.TargetRef.UID != "" {
		return string(e.TargetRef.UID)
	}
	return strings.Join(e.Addresses, ",")
}

func isTrue(b *bool, unset bool) bool {
	if b == nil {
		return unset
	}
	return *b
}

// health judges a service by its ready endpoints; an ExternalName service has none by design.
func health(svc *corev1.Service, ready, total int) string {
	switch {
	case svc.Spec.Type == corev1.ServiceTypeExternalName:
		return healthExternal
	case total == 0:
		return healthNoEndpoints
	case ready == 0:
		return healthUnavailable
	case ready < total:
		return healthDegraded
	default:
		return healthHealthy
	}
}

// externalIP follows kubectl: load-balancer ingress, else spec.externalIPs, else the ExternalName target.
func externalIP(svc *corev1.Service) string {
	var ips []string
	for _, in := range svc.Status.LoadBalancer.Ingress {
		if in.IP != "" {
			ips = append(ips, in.IP)
		} else if in.Hostname != "" {
			ips = append(ips, in.Hostname)
		}
	}
	switch {
	case len(ips) > 0:
		return strings.Join(ips, ",")
	case len(svc.Spec.ExternalIPs) > 0:
		return strings.Join(svc.Spec.ExternalIPs, ",")
	case svc.Spec.Type == corev1.ServiceTypeExternalName:
		return svc.Spec.ExternalName
	case svc.Spec.Type == corev1.ServiceTypeLoadBalancer:
		return "<pending>"
	}
	return ""
}

// ports renders ports like kubectl, with the node port when there is one: 80:30080/TCP.
func ports(svc *corev1.Service) string {
	out := make([]string, 0, len(svc.Spec.Ports))
	for _, p := range svc.Spec.Ports {
		s := strconv.Itoa(int(p.Port))
		if p.NodePort != 0 {
			s += ":" + strconv.Itoa(int(p.NodePort))
		}
		out = append(out, s+"/"+string(p.Protocol))
	}
	return strings.Join(out, ",")
}

func formatList(out ServiceListOutput) string {
	if len(out.Services) == 0 {
		if out.Namespace != "" {
			return fmt.Sprintf("no services in namespace %q", out.Namespace)
		}
		return "no services found"
	}
	rows := make([][]string, 0, len(out.Services))
	for _, s := range out.Services {
		rows = append(rows, []string{
			s.Namespace, s.Name, s.Type, kube.OrNone(s.ClusterIP), kube.OrNone(s.ExternalIP), kube.OrNone(s.Ports),
			s.Health, fmt.Sprintf("%d/%d", s.Ready, s.Endpoints), kube.Age(s.Created),
		})
	}
	return kube.Table([]string{"NAMESPACE", "NAME", "TYPE", "CLUSTER-IP", "EXTERNAL-IP", "PORTS", "HEALTH", "ENDPOINTS", "AGE"}, rows)
}

// getService returns nil and a note on a miss; the note names the namespace when that is what is missing.
func getService(ctx context.Context, client k8s.Interface, ref kube.ObjectRef) (*corev1.Service, string, error) {
	s, found, note, err := kube.GetNamespaced(ctx, client, ref, client.CoreV1().Services(ref.Namespace).Get)
	if !found {
		return nil, note, err
	}
	return s, "", nil
}

// serviceSlices lists the EndpointSlices the service owns, by the label the EndpointSlice controller sets.
func serviceSlices(ctx context.Context, client k8s.Interface, ref kube.ObjectRef) ([]discoveryv1.EndpointSlice, error) {
	list, err := client.DiscoveryV1().EndpointSlices(ref.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{discoveryv1.LabelServiceName: ref.Name}).String(),
	})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}
