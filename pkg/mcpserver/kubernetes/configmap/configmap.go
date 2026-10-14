// Package configmap serves the read-only configmap tools: roster with sizes and definition with long values cut.
package configmap

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8s "k8s.io/client-go/kubernetes"

	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
)

// maxValueBytes keeps one large value, such as an embedded dashboard, from crowding out the rest of the object.
const maxValueBytes = 2 * 1024

// ConfigMapListItem is one configmap's roster entry; Keys and Bytes count data and binary data together.
type ConfigMapListItem struct {
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	Keys      int       `json:"keys"`
	Bytes     int       `json:"bytes"`
	Created   time.Time `json:"created"`
}

// ConfigMapListOutput is the structured result of configmap_list; Namespace is empty when listing all.
type ConfigMapListOutput struct {
	Namespace  string              `json:"namespace,omitempty"`
	ConfigMaps []ConfigMapListItem `json:"configmaps"`
	// Total counts every match before the cap.
	Total int `json:"total"`
}

// ConfigMapDescribeOutput is the structured result of configmap_describe: one entry per requested configmap, in request order.
type ConfigMapDescribeOutput struct {
	ConfigMaps []kube.Described `json:"configmaps"`
}

type listInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"the namespace to list; omit to list every namespace"`
}

type targetInput struct {
	ConfigMaps []kube.ObjectRef `json:"configmaps" jsonschema:"the configmaps to describe, at most 5"`
}

// Register adds the configmap tools to server.
func Register(server *mcp.Server, c kube.Clients) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "configmap_list",
		Description: "List Kubernetes configmaps with their number of keys and total size. Pass namespace to list one namespace; omit it to list every namespace.",
		Annotations: kube.ReadOnly(),
	}, listHandler(c))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "configmap_describe",
		Description: "Describe up to 5 Kubernetes configmaps in one call: the full object as YAML with every key and value; values over 2 KB are cut and marked with their full size, and binary data shows sizes only. Pass configmaps as {namespace, name}; a configmap that does not exist has found false.",
		Annotations: kube.ReadOnly(),
	}, describeHandler(c))
}

func listHandler(c kube.Clients) mcp.ToolHandlerFor[listInput, ConfigMapListOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in listInput) (*mcp.CallToolResult, ConfigMapListOutput, error) {
		out, err := listConfigMaps(ctx, c.Core, in.Namespace)
		if err != nil {
			return nil, ConfigMapListOutput{}, err
		}
		out.ConfigMaps, out.Total = kube.Cap(out.ConfigMaps, c.ResultLimit())
		return kube.TextResult(formatList(out)), out, nil
	}
}

func describeHandler(c kube.Clients) mcp.ToolHandlerFor[targetInput, ConfigMapDescribeOutput] {
	return kube.PerTarget(
		func(in targetInput) ([]kube.ObjectRef, error) {
			return kube.ObjectTargets("configmaps", in.ConfigMaps, kube.MaxDescribeTargets)
		},
		func(ctx context.Context, ref kube.ObjectRef) (kube.Described, error) {
			return describe(ctx, c.Core, ref)
		},
		kube.FormatDescribed("configmap"),
		func(m []kube.Described) ConfigMapDescribeOutput { return ConfigMapDescribeOutput{ConfigMaps: m} },
	)
}

func listConfigMaps(ctx context.Context, client k8s.Interface, namespace string) (ConfigMapListOutput, error) {
	namespace = strings.TrimSpace(namespace)
	list, err := client.CoreV1().ConfigMaps(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return ConfigMapListOutput{}, err
	}
	if err := kube.CheckNamespace(ctx, client, namespace, len(list.Items)); err != nil {
		return ConfigMapListOutput{}, err
	}
	out := ConfigMapListOutput{Namespace: namespace, ConfigMaps: make([]ConfigMapListItem, 0, len(list.Items))}
	for i := range list.Items {
		m := &list.Items[i]
		item := ConfigMapListItem{Namespace: m.Namespace, Name: m.Name, Keys: len(m.Data) + len(m.BinaryData), Created: m.CreationTimestamp.Time}
		for _, v := range m.Data {
			item.Bytes += len(v)
		}
		for _, v := range m.BinaryData {
			item.Bytes += len(v)
		}
		out.ConfigMaps = append(out.ConfigMaps, item)
	}
	return out, nil
}

// describe renders through an unstructured copy so binary data can show as a size instead of base64.
func describe(ctx context.Context, client k8s.Interface, ref kube.ObjectRef) (kube.Described, error) {
	m, found, note, err := kube.GetNamespaced(ctx, client, ref, client.CoreV1().ConfigMaps(ref.Namespace).Get)
	if !found {
		return kube.Described{Namespace: ref.Namespace, Name: ref.Name, Note: note}, err
	}
	m.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}
	kube.CleanMeta(&m.ObjectMeta)
	for k, v := range m.Data {
		m.Data[k] = truncate(v)
	}
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(m)
	if err != nil {
		return kube.Described{}, err
	}
	if len(m.BinaryData) > 0 {
		sizes := make(map[string]any, len(m.BinaryData))
		for k, v := range m.BinaryData {
			sizes[k] = fmt.Sprintf("<%d bytes of binary data>", len(v))
		}
		obj["binaryData"] = sizes
	}
	text, err := kube.ToYAML(obj)
	if err != nil {
		return kube.Described{}, err
	}
	return kube.Described{Namespace: ref.Namespace, Name: ref.Name, Found: true, YAML: text}, nil
}

// truncate keeps the head of a long value, cut at a character boundary, and says how long it was.
func truncate(v string) string {
	if len(v) <= maxValueBytes {
		return v
	}
	head := v[:maxValueBytes]
	for !utf8.ValidString(head) {
		head = head[:len(head)-1]
	}
	return head + fmt.Sprintf("\n… (cut; %d bytes total)", len(v))
}

func formatList(out ConfigMapListOutput) string {
	if len(out.ConfigMaps) == 0 {
		if out.Namespace != "" {
			return fmt.Sprintf("no configmaps in namespace %q", out.Namespace)
		}
		return "no configmaps found"
	}
	rows := make([][]string, 0, len(out.ConfigMaps))
	for _, m := range out.ConfigMaps {
		rows = append(rows, []string{m.Namespace, m.Name, strconv.Itoa(m.Keys), humanBytes(m.Bytes), kube.Age(m.Created)})
	}
	return kube.Table([]string{"NAMESPACE", "NAME", "KEYS", "SIZE", "AGE"}, rows) +
		kube.CapNote(len(out.ConfigMaps), out.Total, "configmaps")
}

func humanBytes(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fKi", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1fMi", float64(n)/(1024*1024))
	}
}
