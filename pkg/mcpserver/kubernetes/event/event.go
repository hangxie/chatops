// Package event serves the read-only event tool: recent events, filtered by namespace, object, and type.
package event

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	k8s "k8s.io/client-go/kubernetes"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

// maxEvents bounds the rows returned; the newest events explain the current state.
const maxEvents = 50

// kinds maps the names people and models use to the API kind, since the involvedObject.kind selector is exact.
var kinds = map[string]string{
	"pod": "Pod", "pods": "Pod", "node": "Node", "nodes": "Node", "namespace": "Namespace",
	"deployment": "Deployment", "deploy": "Deployment", "replicaset": "ReplicaSet", "rs": "ReplicaSet",
	"statefulset": "StatefulSet", "sts": "StatefulSet", "daemonset": "DaemonSet", "ds": "DaemonSet",
	"job": "Job", "cronjob": "CronJob", "service": "Service", "svc": "Service",
	"pv": "PersistentVolume", "persistentvolume": "PersistentVolume",
	"pvc": "PersistentVolumeClaim", "persistentvolumeclaim": "PersistentVolumeClaim",
	"configmap": "ConfigMap", "cm": "ConfigMap",
}

// Event is one event as kubectl get events shows it; Object is Kind/name.
type Event struct {
	Namespace string    `json:"namespace,omitempty"`
	LastSeen  time.Time `json:"last_seen"`
	Type      string    `json:"type"`
	Reason    string    `json:"reason"`
	Object    string    `json:"object"`
	Count     int32     `json:"count"`
	Message   string    `json:"message"`
}

// EventListOutput is the structured result of event_list; Total counts every match before the cap.
type EventListOutput struct {
	Namespace string  `json:"namespace,omitempty"`
	Kind      string  `json:"kind,omitempty"`
	Name      string  `json:"name,omitempty"`
	Type      string  `json:"type,omitempty"`
	Total     int     `json:"total"`
	Events    []Event `json:"events"`
}

type listInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"only events in this namespace; omit for every namespace (cluster-scoped objects such as nodes and persistent volumes record theirs in default)"`
	Kind      string `json:"kind,omitempty" jsonschema:"only events about this kind of object, such as Pod, Deployment, Node, or PersistentVolume"`
	Name      string `json:"name,omitempty" jsonschema:"only events about the object with this name"`
	Type      string `json:"type,omitempty" jsonschema:"Warning or Normal; omit for both"`
}

// Register adds the event tool to server.
func Register(server *mcp.Server, c kube.Clients) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "event_list",
		Description: "List recent Kubernetes events, newest first and at most 50, with time, type, reason, object, count, and message. Filter by namespace, by object kind and name (events about one pod, deployment, node, persistent volume, and so on), and by type Warning or Normal. The cluster keeps events for about an hour.",
		Annotations: kube.ReadOnly(),
	}, listHandler(c))
}

func listHandler(c kube.Clients) mcp.ToolHandlerFor[listInput, EventListOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in listInput) (*mcp.CallToolResult, EventListOutput, error) {
		out, err := listEvents(ctx, c.Core, in)
		if err != nil {
			return nil, EventListOutput{}, err
		}
		return kube.TextResult(formatList(out)), out, nil
	}
}

// listEvents filters on the server with event field selectors, then keeps the newest maxEvents.
func listEvents(ctx context.Context, client k8s.Interface, in listInput) (EventListOutput, error) {
	out := EventListOutput{
		Namespace: strings.TrimSpace(in.Namespace), Kind: normalizeKind(in.Kind), Name: strings.TrimSpace(in.Name),
	}
	var err error
	if out.Type, err = normalizeType(in.Type); err != nil {
		return EventListOutput{}, err
	}
	selector := fields.Set{}
	for field, value := range map[string]string{"involvedObject.name": out.Name, "type": out.Type} {
		if value != "" {
			selector[field] = value
		}
	}
	// A kind outside the known names may be spelled as kubectl prints it, lowercase, so it is matched here instead.
	knownKind := out.Kind != "" && kinds[strings.ToLower(out.Kind)] == out.Kind
	if knownKind {
		selector["involvedObject.kind"] = out.Kind
	}
	list, err := client.CoreV1().Events(out.Namespace).List(ctx, metav1.ListOptions{FieldSelector: selector.AsSelector().String()})
	if err != nil {
		return EventListOutput{}, err
	}
	if err := kube.CheckNamespace(ctx, client, out.Namespace, len(list.Items)); err != nil {
		return EventListOutput{}, err
	}
	out.Events = make([]Event, 0, min(len(list.Items), maxEvents))
	for i := range list.Items {
		e := &list.Items[i]
		if out.Kind != "" && !knownKind && !strings.EqualFold(e.InvolvedObject.Kind, out.Kind) {
			continue
		}
		out.Events = append(out.Events, Event{
			Namespace: e.Namespace, LastSeen: lastSeen(e), Type: e.Type, Reason: e.Reason,
			Object: e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name, Count: max(e.Count, 1), Message: e.Message,
		})
	}
	out.Total = len(out.Events)
	sort.SliceStable(out.Events, func(a, b int) bool { return out.Events[a].LastSeen.After(out.Events[b].LastSeen) })
	out.Events = out.Events[:min(len(out.Events), maxEvents)]
	return out, nil
}

func normalizeKind(kind string) string {
	kind = strings.TrimSpace(kind)
	if k, ok := kinds[strings.ToLower(kind)]; ok {
		return k
	}
	return kind
}

func normalizeType(t string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "", "all":
		return "", nil
	case "warning":
		return corev1.EventTypeWarning, nil
	case "normal":
		return corev1.EventTypeNormal, nil
	}
	return "", fmt.Errorf("type must be Warning or Normal, got %q", t)
}

// lastSeen follows kubectl: a series' last observation, else the last timestamp, else when it was first recorded.
func lastSeen(e *corev1.Event) time.Time {
	switch {
	case e.Series != nil && !e.Series.LastObservedTime.IsZero():
		return e.Series.LastObservedTime.Time
	case !e.LastTimestamp.IsZero():
		return e.LastTimestamp.Time
	case !e.EventTime.IsZero():
		return e.EventTime.Time
	default:
		return e.CreationTimestamp.Time
	}
}

func formatList(out EventListOutput) string {
	if len(out.Events) == 0 {
		return "no events" + filters(out)
	}
	header := []string{"LAST SEEN", "TYPE", "REASON", "OBJECT", "COUNT", "MESSAGE"}
	if out.Namespace == "" {
		header = append([]string{"NAMESPACE"}, header...)
	}
	rows := make([][]string, 0, len(out.Events))
	for _, e := range out.Events {
		row := []string{kube.Age(e.LastSeen), e.Type, e.Reason, e.Object, strconv.Itoa(int(e.Count)), strings.Join(strings.Fields(e.Message), " ")}
		if out.Namespace == "" {
			row = append([]string{kube.OrNone(e.Namespace)}, row...)
		}
		rows = append(rows, row)
	}
	text := kube.Table(header, rows)
	if out.Total > len(out.Events) {
		text += fmt.Sprintf("\n(showing the newest %d of %d events)", len(out.Events), out.Total)
	}
	return text
}

// filters describes the active filters for the empty message.
func filters(out EventListOutput) string {
	var parts []string
	if out.Type != "" {
		parts = append(parts, "of type "+out.Type)
	}
	if out.Kind != "" || out.Name != "" {
		parts = append(parts, "about "+strings.Trim(out.Kind+"/"+out.Name, "/"))
	}
	if out.Namespace != "" {
		parts = append(parts, fmt.Sprintf("in namespace %q", out.Namespace))
	}
	if len(parts) == 0 {
		return " found"
	}
	return " " + strings.Join(parts, " ")
}
