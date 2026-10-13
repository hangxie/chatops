package kubernetes

import (
	"context"
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
		Description: "Report the health of one or more Kubernetes nodes in one call: Ready condition, memory/disk/PID pressure, schedulable or not, taints, kubelet version, and roles. Pass every node of interest in nodes (names from node_list); a node that does not exist has found false.",
		Annotations: annotations(),
	}, nodeStatusHandler(c))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "node_resources",
		Description: "Report CPU, memory, disk, and pod capacity, allocatable, and current utilization for one or more Kubernetes nodes in one call. Pass every node of interest in nodes (names from node_list); a node that does not exist has found false.",
		Annotations: annotations(),
	}, nodeResourcesHandler(c))
}

type nodeInput struct {
	Nodes []string `json:"nodes" jsonschema:"the node names to report on, all in one call"`
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
		names, err := targets("nodes", in.Nodes)
		if err != nil {
			return nil, NodeStatusOutput{}, err
		}
		nodes, err := fetchAll(ctx, names, func(ctx context.Context, name string) (NodeStatus, error) {
			return getNodeStatus(ctx, c.core, name)
		})
		if err != nil {
			return nil, NodeStatusOutput{}, err
		}
		return textResult(formatEach(nodes, formatNodeStatus)), NodeStatusOutput{Nodes: nodes}, nil
	}
}

func nodeResourcesHandler(c clients) func(context.Context, *mcp.CallToolRequest, nodeInput) (*mcp.CallToolResult, NodeResourcesOutput, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in nodeInput) (*mcp.CallToolResult, NodeResourcesOutput, error) {
		names, err := targets("nodes", in.Nodes)
		if err != nil {
			return nil, NodeResourcesOutput{}, err
		}
		nodes, err := fetchAll(ctx, names, func(ctx context.Context, name string) (NodeResources, error) {
			return getNodeResources(ctx, c, name)
		})
		if err != nil {
			return nil, NodeResourcesOutput{}, err
		}
		return textResult(formatEach(nodes, formatNodeResources)), NodeResourcesOutput{Nodes: nodes}, nil
	}
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}
