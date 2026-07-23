// Package ping implements a dummy tool.Tool that always answers "pong",
// useful as a liveness check and as the reference implementation of the
// tool interface.
//
// The package exports Scheme and Opener for wiring the tool into a
// tool.Registry under the "ping" URL scheme. The tool has no endpoint
// and takes no credentials, so the URL is bare:
//
//	ping://
//
// The tool takes no arguments; Call.Arguments is ignored.
package ping

import (
	"context"
	"fmt"
	"net/url"

	"github.com/hangxie/chatops/cred"
	"github.com/hangxie/chatops/tool"
)

// Scheme is the URL scheme this tool serves in a tool.Registry.
const Scheme = "ping"

// Descriptor is the tool's self-description for planners; wire it into a
// tool.Backend alongside Scheme and Opener.
var Descriptor = tool.Descriptor{
	Description: "Liveness check; replies \"pong\" to confirm the bot is responsive.",
}

// Opener is the tool.OpenerFunc for this tool: the URL carries no
// endpoint or configuration, and creds is ignored. Any host, path,
// query, userinfo, or non-empty fragment is rejected; a bare trailing
// "#" is parsed by net/url identically to the bare URL and is
// therefore accepted.
func Opener(ctx context.Context, u *url.URL, _ cred.Store) (tool.Tool, error) {
	if u.Host != "" || u.Path != "" || u.RawQuery != "" || u.ForceQuery ||
		u.Opaque != "" || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("ping: URL %q takes no endpoint or configuration", u.String())
	}
	return Open(ctx)
}

// Tool is the dummy ping tool.
type Tool struct{}

// Open returns a ready ping tool; it holds no resources and needs no
// location parameters.
func Open(_ context.Context) (*Tool, error) {
	return &Tool{}, nil
}

// Invoke always answers "pong". Call.Arguments is ignored.
func (t *Tool) Invoke(ctx context.Context, _ tool.Call) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, fmt.Errorf("ping: %w", err)
	}
	return tool.Result{Text: "pong"}, nil
}

// Close releases nothing; the ping tool holds no resources.
func (t *Tool) Close() error {
	return nil
}
