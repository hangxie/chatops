package tool

import (
	"context"
	"fmt"
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
// into NewRegistry.
type Backend struct {
	Scheme string
	Opener OpenerFunc
}

// schemeRE is the URI scheme syntax from RFC 3986 section 3.1.
var schemeRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*$`)

// Registry maps URL schemes to tool implementations. It is immutable
// after construction and therefore safe for concurrent use.
type Registry struct {
	openers map[string]OpenerFunc
}

// NewRegistry builds a Registry serving the given backends. Schemes
// are stored lowercase to match url.Parse, which lowercases schemes.
// The backend list is static wiring, so mistakes in it are programmer
// errors: NewRegistry panics if a scheme is not valid RFC 3986 scheme
// syntax, an opener is nil, or a scheme appears twice.
func NewRegistry(backends ...Backend) *Registry {
	openers := make(map[string]OpenerFunc, len(backends))
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
		openers[scheme] = b.Opener
	}
	return &Registry{openers: openers}
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

// Select returns a registry containing only the named tools. Repeated and
// mixed-case names identify the same tool. An unknown name returns an error
// listing the available choices.
func (r *Registry) Select(names ...string) (*Registry, error) {
	openers := make(map[string]OpenerFunc, len(names))
	for _, name := range names {
		scheme := strings.ToLower(name)
		opener, ok := r.openers[scheme]
		if !ok {
			return nil, fmt.Errorf("tool: unknown tool %q; available tools: %s", name, strings.Join(r.Schemes(), ", "))
		}
		openers[scheme] = opener
	}
	return &Registry{openers: openers}, nil
}

// Open opens the tool instance identified by rawURL, such as
// "kubernetes://prod.example.com:6443?cred-prefix=k8s-prod"; the URL
// scheme selects the tool. Credentials the tool needs are resolved
// from creds by the key names in the package documentation, never
// taken from the URL. creds may be nil when every wired tool takes no
// credentials; openers that need credentials must report an error.
func (r *Registry) Open(ctx context.Context, rawURL string, creds cred.Store) (Tool, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("tool: parse tool URL: %w", err)
	}
	opener, ok := r.openers[u.Scheme]
	if !ok {
		return nil, fmt.Errorf("tool: unknown tool scheme %q", u.Scheme)
	}
	return opener(ctx, u, creds)
}
