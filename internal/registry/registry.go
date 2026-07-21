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
	"github.com/hangxie/chatops/planner"
	planneropenaichat "github.com/hangxie/chatops/planner/openaichatcompletions"
	plannerping "github.com/hangxie/chatops/planner/ping"
	"github.com/hangxie/chatops/tool"
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

// Tool builds the registry of operational tools the binary knows about.
func Tool() *tool.Registry {
	return tool.NewRegistry(
		tool.Backend{Scheme: toolping.Scheme, Opener: toolping.Opener},
		tool.Backend{Scheme: toolstatus.Scheme, Opener: toolstatus.Opener},
	)
}
