// Package mcpserve assembles the built-in operational tools into Model
// Context Protocol servers.
//
// Tools are grouped, and each group becomes its own *mcp.Server: the ping
// group, the service-status group, the kubernetes group. A group is the unit
// of deployment, not just of organization — the engine connects one session
// per group over an in-memory transport, and any single group can later be
// moved out of process without touching its code, because nothing crosses the
// boundary except JSON.
//
// Each tool sub-package exports a group name and a RegisterFunc; callers wire
// the groups they support into a Registry, so a server can be built by name
// (no init() side effects — supported groups are always visible at the wiring
// site):
//
//	reg := mcpserve.NewRegistry(
//		mcpserve.Group{Name: ping.GroupName, Register: ping.Register},
//	)
//	srv, err := reg.Server("ping", mcpserve.Options{})
//
// Group options are non-secret instance configuration carried as URL query
// values (a kubernetes ?context=, say). Credential values are never options;
// groups resolve predefined cred.Key identifiers from the cred.Store.
package mcpserve

import (
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hangxie/chatops/cred"
)

// Version is reported to peers as the built-in servers' implementation
// version. It is deliberately coarse: peers negotiate on the protocol, not on
// the chatops build.
const Version = "v0"

// RegisterFunc adds one group's tools to s. Groups export one for wiring into
// a Registry. opts carries the group's non-secret instance configuration and
// is never nil; creds may be nil when no wired group needs credentials, and a
// group that needs them must report an error.
type RegisterFunc func(s *mcp.Server, creds cred.Store, opts url.Values) error

// Group pairs a group name with the function registering its tools, for
// wiring into NewRegistry.
type Group struct {
	Name     string
	Register RegisterFunc
}

// Options configures one built-in server.
type Options struct {
	// Query is the group's non-secret instance configuration. It may be nil.
	Query url.Values

	// Credentials is passed to the group's RegisterFunc. It may be nil.
	Credentials cred.Store

	// Logger receives a record when a tool panics. A nil Logger discards
	// them; the panic is contained either way.
	Logger *slog.Logger
}

// nameRE is the group-name syntax: the names become MCP server aliases and
// then model-facing function-name prefixes, so they are kept to the character
// set every LLM tool-use API accepts.
var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Registry maps group names to their tool registrations. It is immutable
// after construction and therefore safe for concurrent use.
type Registry struct {
	groups map[string]RegisterFunc
	names  []string
}

// NewRegistry builds a Registry serving the given groups. The group list is
// static wiring, so mistakes in it are programmer errors: NewRegistry panics
// if a name is not valid group-name syntax, a register function is nil, or a
// name appears twice.
func NewRegistry(groups ...Group) *Registry {
	r := &Registry{groups: make(map[string]RegisterFunc, len(groups))}
	for _, g := range groups {
		if !nameRE.MatchString(g.Name) {
			panic(fmt.Sprintf("mcpserve: NewRegistry with invalid group name %q", g.Name))
		}
		if g.Register == nil {
			panic(fmt.Sprintf("mcpserve: NewRegistry with nil register function for group %q", g.Name))
		}
		if _, exists := r.groups[g.Name]; exists {
			panic(fmt.Sprintf("mcpserve: NewRegistry with duplicate group %q", g.Name))
		}
		r.groups[g.Name] = g.Register
		r.names = append(r.names, g.Name)
	}
	sort.Strings(r.names)
	return r
}

// Names returns the registered group names in lexical order. The returned
// slice is a copy and may be modified by the caller.
func (r *Registry) Names() []string {
	return append([]string(nil), r.names...)
}

// Server builds the MCP server for one group. The returned server is not yet
// connected to a transport; the caller runs it, in process or otherwise.
func (r *Registry) Server(name string, opts Options) (*mcp.Server, error) {
	register, ok := r.groups[name]
	if !ok {
		return nil, fmt.Errorf("mcpserve: unknown built-in group %q; available groups: %s", name, strings.Join(r.names, ", "))
	}
	query := opts.Query
	if query == nil {
		query = url.Values{}
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "chatops-" + name, Version: Version}, nil)
	// Contain a panicking tool before anything else sees the request, so one
	// misbehaving tool cannot take the process down with it.
	srv.AddReceivingMiddleware(RecoverMiddleware(name, logger))
	if err := register(srv, opts.Credentials, query); err != nil {
		return nil, fmt.Errorf("mcpserve: build built-in group %q: %w", name, err)
	}
	return srv, nil
}

// ParseSpec splits a built-in group selector into its group name and options.
// A selector is a bare group name ("k8s") or a name carrying query
// configuration ("k8s?context=prod").
func ParseSpec(spec string) (name string, query url.Values, err error) {
	u, err := url.Parse(spec)
	if err != nil {
		return "", nil, fmt.Errorf("mcpserve: parse built-in group %q: %w", spec, err)
	}
	if u.Scheme != "" || u.Host != "" || u.Opaque != "" || u.User != nil || u.Fragment != "" {
		return "", nil, fmt.Errorf("mcpserve: built-in group %q must be a bare name, optionally with ?options (for example k8s?context=prod)", spec)
	}
	if !nameRE.MatchString(u.Path) {
		return "", nil, fmt.Errorf("mcpserve: invalid built-in group name %q", u.Path)
	}
	return u.Path, u.Query(), nil
}

// CheckOptions rejects option keys a group does not understand, so an
// operator's typo fails at startup rather than being silently ignored.
func CheckOptions(group string, opts url.Values, allowed ...string) error {
	permitted := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		permitted[key] = true
	}
	unknown := make([]string, 0, len(opts))
	for key := range opts {
		if !permitted[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	sort.Strings(allowed)
	if len(allowed) == 0 {
		return fmt.Errorf("%s: unknown option %s; this group takes no options", group, strings.Join(unknown, ", "))
	}
	return fmt.Errorf("%s: unknown option %s; supported options: %s", group, strings.Join(unknown, ", "), strings.Join(allowed, ", "))
}
