// Package tools lists the operational tools the binary can use.
package tools

import (
	"encoding/json"
	"fmt"

	"github.com/hangxie/chatops/internal/registry"
)

// Cmd is the kong command for tools.
type Cmd struct {
	JSON bool `short:"j" help:"Output in JSON format." default:"false"`
}

// Run prints the registered tool schemes, one per line, or as a JSON array
// when --json is set.
func (c Cmd) Run() error {
	schemes := registry.Tool().Schemes()

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
