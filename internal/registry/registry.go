// Package registry provides the backend wiring shared by the CLI
// commands, so that commands which run a backend and commands which
// merely list backends always agree on the registered set.
package registry

import (
	"github.com/hangxie/chatops/chat"
	chatslack "github.com/hangxie/chatops/chat/slack"
	"github.com/hangxie/chatops/chat/telnet"
	"github.com/hangxie/chatops/cred"
	"github.com/hangxie/chatops/cred/jsonfile"
	"github.com/hangxie/chatops/mcpserve"
	"github.com/hangxie/chatops/planner"
	planneropenaichat "github.com/hangxie/chatops/planner/openaichatcompletions"
	plannerping "github.com/hangxie/chatops/planner/ping"
	toolk8s "github.com/hangxie/chatops/tool/k8s"
	toolping "github.com/hangxie/chatops/tool/ping"
	toolstatus "github.com/hangxie/chatops/tool/status"
)

// Chat builds the registry of chat backends the binary knows about.
func Chat() *chat.Registry {
	return chat.NewRegistry(
		chat.Backend{Scheme: chatslack.Scheme, Opener: chatslack.Opener},
		chat.Backend{Scheme: telnet.Scheme, Opener: telnet.Opener},
	)
}

// Credential builds the registry of credential stores the binary knows
// about.
func Credential() *cred.Registry {
	return cred.NewRegistry(cred.Backend{Scheme: jsonfile.Scheme, Opener: jsonfile.Opener})
}

// Planner builds the registry of planner backends the binary knows
// about.
func Planner() *planner.Registry {
	return planner.NewRegistry(
		planner.Backend{Scheme: planneropenaichat.Scheme, Opener: planneropenaichat.Opener},
		planner.Backend{Scheme: plannerping.Scheme, Opener: plannerping.Opener},
	)
}

// Builtin builds the registry of built-in MCP tool groups the binary knows
// about. Each group becomes its own MCP server, so any one of them can be
// moved out of process without touching the others.
func Builtin() *mcpserve.Registry {
	return mcpserve.NewRegistry(
		mcpserve.Group{Name: toolk8s.GroupName, Register: toolk8s.Register},
		mcpserve.Group{Name: toolping.GroupName, Register: toolping.Register},
		mcpserve.Group{Name: toolstatus.GroupName, Register: toolstatus.Register},
	)
}
