// Package chats lists the chat backends the binary can connect to.
package chats

import (
	"encoding/json"
	"fmt"

	"github.com/hangxie/chatops/internal/registry"
)

// Cmd is the kong command for chats.
type Cmd struct {
	JSON bool `short:"j" help:"Output in JSON format." default:"false"`
}

// Run prints the registered chat backend schemes, one per line, or as a
// JSON array when --json is set.
func (c Cmd) Run() error {
	schemes := registry.Chat().Schemes()

	if c.JSON {
		buf, _ := json.Marshal(schemes)
		fmt.Println(string(buf))
		return nil
	}

	for _, scheme := range schemes {
		fmt.Println(scheme)
	}
	return nil
}
