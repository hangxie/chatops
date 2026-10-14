package event

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kubetest"
)

var base = time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)

func event(namespace, name, typ, reason, kind, object string, seen time.Time) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Namespace: namespace, Name: name},
		Type:           typ,
		Reason:         reason,
		InvolvedObject: corev1.ObjectReference{Kind: kind, Name: object, Namespace: namespace},
		LastTimestamp:  metav1.NewTime(seen),
		Count:          2,
		Message:        "Back-off restarting\nfailed container",
	}
}

// selectorClient records the field selector of each event list and answers with events.
func selectorClient(events ...*corev1.Event) (*fake.Clientset, *string) {
	client := fake.NewClientset(kubetest.Namespace("argocd"))
	var selector string
	client.PrependReactor("list", "events", func(a k8stesting.Action) (bool, runtime.Object, error) {
		selector = a.(k8stesting.ListAction).GetListRestrictions().Fields.String()
		list := &corev1.EventList{}
		for _, e := range events {
			list.Items = append(list.Items, *e)
		}
		return true, list, nil
	})
	return client, &selector
}

func Test_listEvents_filters(t *testing.T) {
	tests := map[string]struct {
		in       listInput
		selector string
		errMsg   string
	}{
		"no filters":   {in: listInput{}, selector: ""},
		"object":       {in: listInput{Kind: "pod", Name: " server-1 "}, selector: "involvedObject.kind=Pod,involvedObject.name=server-1"},
		"short kind":   {in: listInput{Kind: "PVC"}, selector: "involvedObject.kind=PersistentVolumeClaim"},
		"unknown kind": {in: listInput{Kind: "Certificate", Name: "tls"}, selector: "involvedObject.name=tls"},
		"warnings":     {in: listInput{Type: "warning"}, selector: "type=Warning"},
		"normal":       {in: listInput{Type: "Normal"}, selector: "type=Normal"},
		"all types":    {in: listInput{Type: "all"}, selector: ""},
		"bad type":     {in: listInput{Type: "Error"}, errMsg: `type must be Warning or Normal, got "Error"`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			client, selector := selectorClient(event("argocd", "e1", "Warning", "BackOff", "Pod", "server-1", base))
			_, err := listEvents(context.Background(), client, tc.in)
			if tc.errMsg != "" {
				require.EqualError(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.selector, *selector)
		})
	}
}

func Test_listEvents_unknown_kind_any_case(t *testing.T) {
	client, _ := selectorClient(
		event("default", "e1", "Normal", "Valid", "ClusterSecretStore", "loki", base),
		event("default", "e2", "Normal", "Valid", "SecretStore", "loki", base),
	)
	out, err := listEvents(context.Background(), client, listInput{Kind: "clustersecretstore"})
	require.NoError(t, err)
	require.Len(t, out.Events, 1)
	require.Equal(t, "ClusterSecretStore/loki", out.Events[0].Object)
}

func Test_listEvents_newest_first(t *testing.T) {
	client, _ := selectorClient(
		event("argocd", "old", "Normal", "Pulled", "Pod", "p", base),
		event("argocd", "new", "Normal", "Pulled", "Pod", "p", base.Add(2*time.Second)),
		event("argocd", "mid", "Normal", "Pulled", "Pod", "p", base.Add(time.Second)),
	)

	out, err := listEvents(context.Background(), client, listInput{Namespace: "argocd"})
	require.NoError(t, err)
	require.Len(t, out.Events, 3)
	require.Equal(t, []time.Time{base.Add(2 * time.Second), base.Add(time.Second), base}, []time.Time{out.Events[0].LastSeen, out.Events[1].LastSeen, out.Events[2].LastSeen})
	require.Equal(t, Event{
		Namespace: "argocd", LastSeen: base, Type: "Normal", Reason: "Pulled", Object: "Pod/p",
		Count: 2, Message: "Back-off restarting\nfailed container",
	}, out.Events[2])
}

func Test_tool_keeps_newest_under_cap(t *testing.T) {
	var events []*corev1.Event
	for i := range 5 {
		events = append(events, event("argocd", fmt.Sprintf("e%d", i), "Normal", "Pulled", "Pod", "p", base.Add(time.Duration(i)*time.Second)))
	}
	client, _ := selectorClient(events...)
	session := kubetest.Connect(t, func(s *mcp.Server) { Register(s, kube.Clients{Core: client, MaxResults: 2}) })

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "event_list"})
	require.NoError(t, err)
	require.False(t, result.IsError)
	rows := kubetest.StructuredList(t, result, "events")
	require.Len(t, rows, 2)
	require.EqualValues(t, 5, result.StructuredContent.(map[string]any)["total"])
	require.Equal(t, base.Add(4*time.Second).Format(time.RFC3339), rows[0]["last_seen"])
	require.Contains(t, result.Content[0].(*mcp.TextContent).Text, "(showing the newest 2 of 5 events)")

	tools, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.Contains(t, tools.Tools[0].Description, "at most 2,")
}

func Test_listEvents_namespace_and_errors(t *testing.T) {
	client := fake.NewClientset(kubetest.Namespace("argocd"))
	_, err := listEvents(context.Background(), client, listInput{Namespace: "nope"})
	require.EqualError(t, err, `namespace "nope" does not exist`)

	out, err := listEvents(context.Background(), client, listInput{Namespace: "argocd"})
	require.NoError(t, err)
	require.Empty(t, out.Events)

	client.PrependReactor("list", "events", kubetest.Forbidden("list", "events"))
	_, err = listEvents(context.Background(), client, listInput{})
	require.ErrorContains(t, err, "list denied")
}

func Test_lastSeen(t *testing.T) {
	series := event("a", "e", "Normal", "r", "Pod", "p", base)
	series.Series = &corev1.EventSeries{LastObservedTime: metav1.NewMicroTime(base.Add(time.Hour))}
	eventTime := &corev1.Event{EventTime: metav1.NewMicroTime(base.Add(2 * time.Hour))}
	created := &corev1.Event{ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(base.Add(3 * time.Hour))}}

	require.Equal(t, base.Add(time.Hour), lastSeen(series))
	require.Equal(t, base, lastSeen(event("a", "e", "Normal", "r", "Pod", "p", base)))
	require.Equal(t, base.Add(2*time.Hour), lastSeen(eventTime))
	require.Equal(t, base.Add(3*time.Hour), lastSeen(created))
}

func Test_formatList(t *testing.T) {
	kubetest.FreezeNow(t, &kube.Now, base.Add(5*time.Minute))
	e := Event{Namespace: "argocd", LastSeen: base, Type: "Warning", Reason: "BackOff", Object: "Pod/server-1", Count: 3, Message: "Back-off restarting\nfailed container"}

	text := formatList(EventListOutput{Total: 1, Events: []Event{e}})
	require.Regexp(t, `NAMESPACE\s+LAST SEEN\s+TYPE\s+REASON\s+OBJECT\s+COUNT\s+MESSAGE`, text)
	require.Regexp(t, `argocd\s+5m\s+Warning\s+BackOff\s+Pod/server-1\s+3\s+Back-off restarting failed container`, text)

	text = formatList(EventListOutput{Namespace: "argocd", Total: 70, Events: []Event{e}})
	require.False(t, strings.HasPrefix(text, "NAMESPACE"))
	require.Contains(t, text, "\n(showing the newest 1 of 70 events)")

	tests := map[string]struct {
		out  EventListOutput
		want string
	}{
		"anywhere":  {want: "no events found"},
		"filtered":  {out: EventListOutput{Namespace: "argocd", Kind: "Pod", Name: "server-1", Type: "Warning"}, want: `no events of type Warning about Pod/server-1 in namespace "argocd"`},
		"kind only": {out: EventListOutput{Kind: "Node"}, want: "no events about Node"},
		"name only": {out: EventListOutput{Name: "worker-4"}, want: "no events about worker-4"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, formatList(tc.out))
		})
	}
}

func Test_tool(t *testing.T) {
	client, _ := selectorClient(event("argocd", "e1", "Warning", "BackOff", "Pod", "server-1", base))
	session := kubetest.Connect(t, func(s *mcp.Server) { Register(s, kube.Clients{Core: client}) })

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "event_list", Arguments: map[string]any{"kind": "pod", "name": "server-1"}})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Contains(t, result.Content[0].(*mcp.TextContent).Text, "BackOff")
	require.Len(t, kubetest.StructuredList(t, result, "events"), 1)

	result, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "event_list", Arguments: map[string]any{"type": "bogus"}})
	require.NoError(t, err)
	require.True(t, result.IsError)
}
