// Package kubernetes serves read-only Kubernetes inspection tools over MCP, one subpackage per resource.
package kubernetes

import (
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hangxie/chatops/internal/version"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/daemonset"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/deployment"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/internal/kube"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/namespace"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/node"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/pod"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/replicaset"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/service"
	"github.com/hangxie/chatops/pkg/mcpserver/kubernetes/statefulset"
)

// requestTimeout is the per-request backstop set on the client, independent of the caller's context deadline.
const requestTimeout = 30 * time.Second

// Config selects how the Kubernetes client connects; an empty Config uses in-cluster then the default kubeconfig.
type Config = kube.Config

// Register builds the Kubernetes clients from cfg and adds every resource's read-only tools to server.
func Register(server *mcp.Server, cfg Config) error {
	c, err := kube.NewClients(cfg, requestTimeout, "chatops-mcp/"+version.String())
	if err != nil {
		return err
	}
	node.Register(server, c)
	namespace.Register(server, c)
	deployment.Register(server, c)
	pod.Register(server, c)
	statefulset.Register(server, c)
	daemonset.Register(server, c)
	replicaset.Register(server, c)
	service.Register(server, c)
	return nil
}
