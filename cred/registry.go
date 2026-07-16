package cred

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
)

// OpenerFunc connects to the credential store described by u and
// returns a Store. Backends provide one to Register; the URL scheme
// has already been matched, and interpretation of the rest of the URL
// is backend-specific.
type OpenerFunc func(ctx context.Context, u *url.URL) (Store, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]OpenerFunc{}

	// schemeRE is the URI scheme syntax from RFC 3986 section 3.1.
	schemeRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*$`)
)

// Register makes opener available to Open under the given URL scheme.
// It is intended to be called from a backend package's init function,
// so a blank import of the backend enables its scheme. The scheme is
// stored lowercase to match url.Parse, which lowercases schemes.
// Register panics if the scheme is not valid RFC 3986 scheme syntax,
// opener is nil, or the scheme is already registered.
func Register(scheme string, opener OpenerFunc) {
	if !schemeRE.MatchString(scheme) {
		panic(fmt.Sprintf("cred: Register with invalid scheme %q", scheme))
	}
	if opener == nil {
		panic(fmt.Sprintf("cred: Register(%q) with nil opener", scheme))
	}
	scheme = strings.ToLower(scheme)
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[scheme]; exists {
		panic(fmt.Sprintf("cred: scheme %q already registered", scheme))
	}
	registry[scheme] = opener
}

// Open connects to the credential store identified by rawURL, such as
// "json-file://path/to/creds.json". The URL scheme selects the
// backend, which must have been registered — typically by importing
// its package. Credentials for accessing the store itself (e.g.
// AWS_ACCESS_KEY_ID, VAULT_TOKEN) are taken from each backend's
// standard environment variables, never from the URL.
func Open(ctx context.Context, rawURL string) (Store, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("cred: parse store URL: %w", err)
	}
	registryMu.RLock()
	opener, ok := registry[u.Scheme]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("cred: unknown credential store scheme %q", u.Scheme)
	}
	return opener(ctx, u)
}
