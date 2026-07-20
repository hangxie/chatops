// Package planners lists the planner backends the binary can use.
package planners

import (
	"encoding/json"
	"fmt"

	"github.com/hangxie/chatops/internal/registry"
)

// Cmd is the kong command for planners.
type Cmd struct {
	JSON bool `short:"j" help:"Output in JSON format." default:"false"`
}

// Run prints the registered planner backend schemes, one per line, or as a
// JSON array when --json is set.
func (c Cmd) Run() error {
	schemes := registry.Planner().Schemes()

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
