package kubernetes

import (
	"context"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hangxie/chatops/internal/version"
)

// requestTimeout is the per-request backstop set on the client, independent of the caller's context deadline.
const requestTimeout = 30 * time.Second

// Config selects how the Kubernetes client connects; an empty Config uses in-cluster then the default kubeconfig.
type Config struct {
	Kubeconfig string
	Context    string
	// NodeDiskUsage enables node_resources disk utilization via the kubelet Summary API; it needs nodes/proxy RBAC.
	NodeDiskUsage bool
}

// Register builds the Kubernetes clients from cfg and adds the read-only node tools to server.
func Register(server *mcp.Server, cfg Config) error {
	c, err := newClients(cfg, requestTimeout, "chatops-mcp/"+version.String())
	if err != nil {
		return err
	}
	register(server, c)
	return nil
}

func register(server *mcp.Server, c clients) {
	openWorld := true
	annotations := func() *mcp.ToolAnnotations {
		return &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &openWorld}
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "node_list",
		Description: "List the Kubernetes cluster's nodes with their status, roles, age, and kubelet version. Takes no arguments.",
		Annotations: annotations(),
	}, nodeListHandler(c))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "node_status",
		Description: "Report one Kubernetes node's health: Ready condition, memory/disk/PID pressure, schedulable or not, taints, kubelet version, and roles. If the node does not exist, found is false.",
		Annotations: annotations(),
	}, nodeStatusHandler(c))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "node_resources",
		Description: "Report one Kubernetes node's CPU, memory, disk, and pod capacity, allocatable, and current utilization. If the node does not exist, found is false.",
		Annotations: annotations(),
	}, nodeResourcesHandler(c))
}

type nodeInput struct {
	Node string `json:"node" jsonschema:"the node name"`
}

func nodeListHandler(c clients) func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, NodeListOutput, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, NodeListOutput, error) {
		out, err := listNodes(ctx, c.core)
		if err != nil {
			return nil, NodeListOutput{}, err
		}
		return textResult(formatNodeList(out)), out, nil
	}
}

func nodeStatusHandler(c clients) func(context.Context, *mcp.CallToolRequest, nodeInput) (*mcp.CallToolResult, NodeStatusOutput, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in nodeInput) (*mcp.CallToolResult, NodeStatusOutput, error) {
		if in.Node == "" {
			return nil, NodeStatusOutput{}, errors.New("node is required")
		}
		out, err := getNodeStatus(ctx, c.core, in.Node)
		if err != nil {
			return nil, NodeStatusOutput{}, err
		}
		return textResult(formatNodeStatus(out)), out, nil
	}
}

func nodeResourcesHandler(c clients) func(context.Context, *mcp.CallToolRequest, nodeInput) (*mcp.CallToolResult, NodeResourcesOutput, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in nodeInput) (*mcp.CallToolResult, NodeResourcesOutput, error) {
		if in.Node == "" {
			return nil, NodeResourcesOutput{}, errors.New("node is required")
		}
		out, err := getNodeResources(ctx, c, in.Node)
		if err != nil {
			return nil, NodeResourcesOutput{}, err
		}
		return textResult(formatNodeResources(out)), out, nil
	}
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}
