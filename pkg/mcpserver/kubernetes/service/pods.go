package service

import (
	"context"
	"fmt"
	"strings"

	discoveryv1 "k8s.io/api/discovery/v1"
	k8s "k8s.io/client-go/kubernetes"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

// ServicePodsOutput is the structured result of service_pods: one entry per requested service, in request order.
type ServicePodsOutput struct {
	Services []ServicePods `json:"services"`
}

// ServicePods is one service's endpoints; Pod is empty for an endpoint that is not a pod.
type ServicePods struct {
	Namespace string     `json:"namespace"`
	Name      string     `json:"name"`
	Found     bool       `json:"found"`
	Note      string     `json:"note,omitempty"`
	Health    string     `json:"health,omitempty"`
	Type      string     `json:"type,omitempty"`
	Endpoints []Endpoint `json:"endpoints,omitempty"`
	// Total counts every endpoint before the cap.
	Total int `json:"total,omitempty"`
}

// Endpoint is one backend of a service, merged across the EndpointSlices that list it.
type Endpoint struct {
	Pod         string   `json:"pod,omitempty"`
	Addresses   []string `json:"addresses"`
	Ready       bool     `json:"ready"`
	Serving     bool     `json:"serving"`
	Terminating bool     `json:"terminating"`
	Node        string   `json:"node,omitempty"`
}

// getPods reads the service's EndpointSlices, the pods traffic actually reaches, rather than its selector.
func getPods(ctx context.Context, client k8s.Interface, ref kube.ObjectRef) (ServicePods, error) {
	out := ServicePods{Namespace: ref.Namespace, Name: ref.Name}
	svc, note, err := getService(ctx, client, ref)
	if err != nil || svc == nil {
		out.Note = note
		return out, err
	}
	slices, err := serviceSlices(ctx, client, ref)
	if err != nil {
		return ServicePods{}, err
	}
	out.Endpoints = mergeEndpoints(slices)
	ready, total := countEndpoints(out.Endpoints)
	out.Found, out.Health, out.Type = true, health(svc, ready, total), string(svc.Spec.Type)
	return out, nil
}

func endpoint(e discoveryv1.Endpoint) Endpoint {
	out := Endpoint{
		Addresses: append([]string(nil), e.Addresses...), Ready: isTrue(e.Conditions.Ready, true), Serving: isTrue(e.Conditions.Serving, true),
		Terminating: isTrue(e.Conditions.Terminating, false),
	}
	if e.TargetRef != nil && e.TargetRef.Kind == "Pod" {
		out.Pod = e.TargetRef.Name
	}
	if e.NodeName != nil {
		out.Node = *e.NodeName
	}
	return out
}

func formatPods(out ServicePods) string {
	target := out.Namespace + "/" + out.Name
	if !out.Found {
		return kube.Miss("service", target, out.Note)
	}
	head := fmt.Sprintf("%s %s (%s)", target, out.Health, out.Type)
	if len(out.Endpoints) == 0 {
		return head + "\nno endpoints"
	}
	rows := make([][]string, 0, len(out.Endpoints))
	for _, e := range out.Endpoints {
		rows = append(rows, []string{
			kube.OrNone(e.Pod), strings.Join(e.Addresses, ","), yesNo(e.Ready), yesNo(e.Serving),
			yesNo(e.Terminating), kube.OrNone(e.Node),
		})
	}
	return head + "\n" + kube.Table([]string{"POD", "ADDRESS", "READY", "SERVING", "TERMINATING", "NODE"}, rows) +
		kube.CapNote(len(out.Endpoints), out.Total, "endpoints")
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
