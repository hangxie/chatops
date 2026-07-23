package tool

import (
	"context"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/hangxie/chatops/cred"
)

// OpenerFunc opens the tool instance described by u, resolving any
// credentials it needs from creds. Tools export one for wiring into a
// Registry; the URL scheme has already been matched, and
// interpretation of the rest of the URL is tool-specific. Tools that
// need no credentials ignore creds.
type OpenerFunc func(ctx context.Context, u *url.URL, creds cred.Store) (Tool, error)

// Backend pairs a URL scheme with the opener serving it, for wiring
// into NewRegistry. Descriptor is required: every tool self-describes so
// planners can offer it a precise, typed function definition.
type Backend struct {
	Scheme     string
	Opener     OpenerFunc
	Descriptor *Descriptor
}

// schemeRE is the URI scheme syntax from RFC 3986 section 3.1.
var schemeRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*$`)

// Registry maps URL schemes to tool implementations. It is immutable
// after construction and therefore safe for concurrent use.
type Registry struct {
	openers     map[string]OpenerFunc
	descriptors map[string]Descriptor
	// configured holds operator-supplied tool URLs keyed by scheme, set by
	// Select. When a scheme is configured, Open uses the stored URL in place
	// of the caller's, so operator configuration (a k8s ?context=, say)
	// reaches the opener even though the planner emits a bare scheme URL.
	configured map[string]*url.URL
}

// NewRegistry builds a Registry serving the given backends. Schemes
// are stored lowercase to match url.Parse, which lowercases schemes.
// The backend list is static wiring, so mistakes in it are programmer
// errors: NewRegistry panics if a scheme is not valid RFC 3986 scheme
// syntax, an opener is nil, or a scheme appears twice.
func NewRegistry(backends ...Backend) *Registry {
	openers := make(map[string]OpenerFunc, len(backends))
	descriptors := make(map[string]Descriptor, len(backends))
	for _, b := range backends {
		if !schemeRE.MatchString(b.Scheme) {
			panic(fmt.Sprintf("tool: NewRegistry with invalid scheme %q", b.Scheme))
		}
		if b.Opener == nil {
			panic(fmt.Sprintf("tool: NewRegistry with nil opener for scheme %q", b.Scheme))
		}
		scheme := strings.ToLower(b.Scheme)
		if _, exists := openers[scheme]; exists {
			panic(fmt.Sprintf("tool: NewRegistry with duplicate scheme %q", scheme))
		}
		if b.Descriptor == nil {
			panic(fmt.Sprintf("tool: NewRegistry with nil descriptor for scheme %q", b.Scheme))
		}
		validateDescriptor(scheme, *b.Descriptor)
		openers[scheme] = b.Opener
		descriptors[scheme] = b.Descriptor.Clone()
	}
	return &Registry{openers: openers, descriptors: descriptors}
}

// validateDescriptor panics on an invalid descriptor: it is a programmer
// error in the static backend list, caught here rather than at the model API.
func validateDescriptor(scheme string, d Descriptor) {
	if err := d.Validate(); err != nil {
		panic(fmt.Sprintf("tool: NewRegistry with descriptor for scheme %q: %v", scheme, err))
	}
}

// Schemes returns the registered tool URL schemes in lexical order. The
// returned slice is a copy and may be modified by the caller.
func (r *Registry) Schemes() []string {
	schemes := make([]string, 0, len(r.openers))
	for scheme := range r.openers {
		schemes = append(schemes, scheme)
	}
	sort.Strings(schemes)
	return schemes
}

// Descriptor returns the descriptor registered for scheme, matched
// case-insensitively. The second result is false only for an unknown scheme.
func (r *Registry) Descriptor(scheme string) (Descriptor, bool) {
	d, ok := r.descriptors[strings.ToLower(scheme)]
	if !ok {
		return Descriptor{}, false
	}
	return d.Clone(), true
}

// Select returns a registry containing only the named tools, carrying
// over each tool's descriptor. Each selector is either a bare tool name
// ("k8s-get") or a full tool URL carrying operator configuration
// ("k8s-get://?context=prod"); the scheme selects the tool, and a
// configured URL is remembered so Open serves it in place of the caller's
// bare scheme URL. Repeated and mixed-case selectors identify the same
// tool, last configuration winning. An unparseable selector, or one whose
// scheme names no tool, returns an error listing the available choices.
func (r *Registry) Select(selectors ...string) (*Registry, error) {
	openers := make(map[string]OpenerFunc, len(selectors))
	descriptors := make(map[string]Descriptor, len(selectors))
	configured := make(map[string]*url.URL, len(selectors))
	for _, sel := range selectors {
		scheme, configuredURL, err := parseSelector(sel)
		if err != nil {
			return nil, err
		}
		opener, ok := r.openers[scheme]
		if !ok {
			return nil, fmt.Errorf("tool: unknown tool %q; available tools: %s", scheme, strings.Join(r.Schemes(), ", "))
		}
		openers[scheme] = opener
		if d, ok := r.descriptors[scheme]; ok {
			descriptors[scheme] = d.Clone()
		}
		if configuredURL != nil {
			configured[scheme] = configuredURL
		} else {
			delete(configured, scheme)
		}
	}
	return &Registry{openers: openers, descriptors: descriptors, configured: configured}, nil
}

// Configure returns a copy of r with operator configuration attached for the
// tools named by the given URLs, leaving the exposed set unchanged. Unlike
// Select it neither adds nor removes tools: it only records how the already
// exposed ones are opened, so configuring a tool need not re-list the rest.
// Each argument must be a tool URL (a bare name has nothing to configure)
// whose scheme names a tool in r; later URLs override earlier ones for the
// same scheme. An unparseable argument, a bare name, or a scheme naming no
// exposed tool returns an error listing the available choices.
func (r *Registry) Configure(toolURLs ...string) (*Registry, error) {
	configured := make(map[string]*url.URL, len(r.configured)+len(toolURLs))
	maps.Copy(configured, r.configured)
	for _, raw := range toolURLs {
		scheme, configuredURL, err := parseSelector(raw)
		if err != nil {
			return nil, err
		}
		if configuredURL == nil {
			return nil, fmt.Errorf("tool: %q is not a tool URL; configuration needs a scheme, like %q", raw, raw+"://")
		}
		if _, ok := r.openers[scheme]; !ok {
			return nil, fmt.Errorf("tool: cannot configure unexposed tool %q; available tools: %s", scheme, strings.Join(r.Schemes(), ", "))
		}
		configured[scheme] = configuredURL
	}
	openers := make(map[string]OpenerFunc, len(r.openers))
	maps.Copy(openers, r.openers)
	descriptors := make(map[string]Descriptor, len(r.descriptors))
	for scheme, d := range r.descriptors {
		descriptors[scheme] = d.Clone()
	}
	return &Registry{openers: openers, descriptors: descriptors, configured: configured}, nil
}

// parseSelector splits a Select selector into a lowercase scheme and, for
// the URL form, the parsed URL carrying operator configuration. A bare name
// yields a nil configured URL. url.Parse lowercases the scheme, so a bare
// name is lowercased to match.
func parseSelector(sel string) (scheme string, configuredURL *url.URL, err error) {
	u, err := url.Parse(sel)
	if err != nil {
		return "", nil, fmt.Errorf("tool: parse tool selector %q: %w", sel, err)
	}
	if u.Scheme == "" {
		return strings.ToLower(sel), nil, nil
	}
	return u.Scheme, u, nil
}

// Open opens the tool instance identified by rawURL, such as
// "kubernetes://prod.example.com:6443"; the URL scheme selects the tool.
// Credentials the tool needs are resolved from creds using predefined
// cred.Key identifiers, never taken from the URL. creds may be nil when the
// selected tool takes no credentials; openers that need credentials must
// report an error.
func (r *Registry) Open(ctx context.Context, rawURL string, creds cred.Store) (Tool, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("tool: parse tool URL: %w", err)
	}
	opener, ok := r.openers[u.Scheme]
	if !ok {
		return nil, fmt.Errorf("tool: unknown tool scheme %q", u.Scheme)
	}
	// An operator-configured URL for this scheme replaces the caller's,
	// carrying selection details (a k8s ?context=, say) the planner omits. A
	// copy keeps each Open isolated, so an opener that mutates its URL cannot
	// disturb a concurrent one.
	if cfg, ok := r.configured[u.Scheme]; ok {
		clone := *cfg
		u = &clone
	}
	return opener(ctx, u, creds)
}
