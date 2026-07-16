// Package version reports build metadata injected at link time.
package version

import (
	"encoding/json"
	"fmt"
)

var (
	// semantic version
	version string
	// build time in ISO-8601 format
	build string
	// where the executable came from, can be:
	// - "source" or "" for build from source
	// - "github" for from github release
	source string
)

// Cmd is the kong command for version.
type Cmd struct {
	JSON      bool `short:"j" help:"Output in JSON format." default:"false"`
	All       bool `short:"a" help:"Output all version details." default:"false"`
	BuildTime bool `short:"b" help:"Output build time." default:"false"`
	Source    bool `short:"s" help:"Output source of the executable." default:"false"`
}

// Run prints the version details.
func (c Cmd) Run() error {
	if c.All {
		c.BuildTime = true
		c.Source = true
	}

	// fall back to a placeholder for binaries built without link-time
	// metadata (e.g. go run)
	ver := version
	if ver == "" {
		ver = "(devel)"
	}

	if !c.JSON {
		fmt.Println(ver)
		if c.BuildTime {
			fmt.Println(build)
		}
		if c.Source {
			fmt.Println(source)
		}
		return nil
	}

	v := struct {
		Version   string
		BuildTime *string `json:",omitempty"`
		Source    *string `json:",omitempty"`
	}{
		Version: ver,
	}
	if c.BuildTime {
		v.BuildTime = &build
	}
	if c.Source {
		v.Source = &source
	}
	buf, _ := json.Marshal(v)
	fmt.Println(string(buf))

	return nil
}
