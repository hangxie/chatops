// Package builtin turns operator-supplied built-in group selectors into
// in-process MCP server specs, shared by the commands that need a tool host.
package builtin

import (
	"fmt"

	"github.com/hangxie/chatops/cred"
	"github.com/hangxie/chatops/internal/registry"
	"github.com/hangxie/chatops/mcphost"
	"github.com/hangxie/chatops/mcpserve"
)

// Servers builds one in-process MCP server per selected built-in group.
//
// Each selector is a bare group name ("k8s") or a name carrying options
// ("k8s?context=prod"); an empty selector list selects every group with
// default options. Groups are separate servers so any one of them can later
// be moved out of process on its own, and they are unaliased because the
// built-in tool names are already distinct across groups.
func Servers(selectors []string, creds cred.Store) ([]mcphost.ServerSpec, error) {
	reg := registry.Builtin()
	if len(selectors) == 0 {
		selectors = reg.Names()
	}
	specs := make([]mcphost.ServerSpec, 0, len(selectors))
	seen := make(map[string]bool, len(selectors))
	for _, selector := range selectors {
		name, query, err := mcpserve.ParseSpec(selector)
		if err != nil {
			return nil, err
		}
		if seen[name] {
			return nil, fmt.Errorf("built-in group %q selected more than once", name)
		}
		seen[name] = true
		srv, err := reg.Server(name, mcpserve.Options{Query: query, Credentials: creds})
		if err != nil {
			return nil, err
		}
		specs = append(specs, mcphost.InProcess(name, "", srv))
	}
	return specs, nil
}
