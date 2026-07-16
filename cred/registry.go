package cred

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// OpenerFunc connects to the credential store described by u and
// returns a Store. Backends export one for wiring into a Registry;
// the URL scheme has already been matched, and interpretation of the
// rest of the URL is backend-specific.
type OpenerFunc func(ctx context.Context, u *url.URL) (Store, error)

// Backend pairs a URL scheme with the opener serving it, for wiring
// into NewRegistry.
type Backend struct {
	Scheme string
	Opener OpenerFunc
}

// schemeRE is the URI scheme syntax from RFC 3986 section 3.1.
var schemeRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*$`)

// Registry maps URL schemes to credential store backends. It is
// immutable after construction and therefore safe for concurrent use.
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
			panic(fmt.Sprintf("cred: NewRegistry with invalid scheme %q", b.Scheme))
		}
		if b.Opener == nil {
			panic(fmt.Sprintf("cred: NewRegistry with nil opener for scheme %q", b.Scheme))
		}
		scheme := strings.ToLower(b.Scheme)
		if _, exists := openers[scheme]; exists {
			panic(fmt.Sprintf("cred: NewRegistry with duplicate scheme %q", scheme))
		}
		openers[scheme] = b.Opener
	}
	return &Registry{openers: openers}
}

// Open connects to the credential store identified by rawURL, such as
// "json-file://path/to/creds.json"; the URL scheme selects the
// backend. Credentials for accessing the store itself (e.g.
// AWS_ACCESS_KEY_ID, VAULT_TOKEN) are taken from each backend's
// standard environment variables, never from the URL.
func (r *Registry) Open(ctx context.Context, rawURL string) (Store, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("cred: parse store URL: %w", err)
	}
	opener, ok := r.openers[u.Scheme]
	if !ok {
		return nil, fmt.Errorf("cred: unknown credential store scheme %q", u.Scheme)
	}
	return opener(ctx, u)
}
