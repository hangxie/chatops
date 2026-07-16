package cred_test

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/cred"
)

// schemeCounter makes registered scheme names unique so repeated runs
// in one process (go test -count=N) do not collide in the global
// registry.
var schemeCounter atomic.Int64

func uniqueScheme(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, schemeCounter.Add(1))
}

func fakeOpener(store cred.Store, err error) cred.OpenerFunc {
	return func(_ context.Context, _ *url.URL) (cred.Store, error) {
		return store, err
	}
}

func Test_Register_invalid_arguments(t *testing.T) {
	testCases := map[string]struct {
		scheme string
		opener cred.OpenerFunc
	}{
		"empty-scheme":     {scheme: "", opener: fakeOpener(nil, nil)},
		"nil-opener":       {scheme: uniqueScheme("nil-opener"), opener: nil},
		"leading-digit":    {scheme: "1abc", opener: fakeOpener(nil, nil)},
		"space":            {scheme: "json file", opener: fakeOpener(nil, nil)},
		"underscore":       {scheme: "json_file", opener: fakeOpener(nil, nil)},
		"colon-and-slash":  {scheme: "json://", opener: fakeOpener(nil, nil)},
		"non-ascii-letter": {scheme: "sché", opener: fakeOpener(nil, nil)},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			require.Panics(t, func() {
				cred.Register(tc.scheme, tc.opener)
			})
		})
	}
}

func Test_Register_duplicate_scheme_panics(t *testing.T) {
	scheme := uniqueScheme("dup")
	cred.Register(scheme, fakeOpener(nil, nil))
	require.Panics(t, func() {
		cred.Register(scheme, fakeOpener(nil, nil))
	})
	// Same scheme in a different case is still a duplicate.
	require.Panics(t, func() {
		cred.Register(strings.ToUpper(scheme), fakeOpener(nil, nil))
	})
}

func Test_Register_normalizes_scheme_to_lowercase(t *testing.T) {
	scheme := uniqueScheme("mixed")
	store := &fakeStore{creds: map[string]string{}}
	cred.Register(strings.ToUpper(scheme), fakeOpener(store, nil))

	// url.Parse lowercases the scheme, so lookup must be lowercase
	// regardless of how the scheme was registered or written in the URL.
	for _, rawURL := range []string{
		scheme + "://whatever",
		strings.ToUpper(scheme) + "://whatever",
	} {
		opened, err := cred.Open(context.Background(), rawURL)
		require.NoError(t, err)
		require.Same(t, cred.Store(store), opened)
	}
}

func Test_Open(t *testing.T) {
	scheme := uniqueScheme("fake")
	store := &fakeStore{creds: map[string]string{"db-password": "hunter2"}}
	cred.Register(scheme, fakeOpener(store, nil))

	testCases := map[string]struct {
		url    string
		errMsg string
	}{
		"registered-scheme":   {url: scheme + "://whatever"},
		"unregistered-scheme": {url: "no-such-backend://whatever", errMsg: `unknown credential store scheme "no-such-backend"`},
		"no-scheme":           {url: "/just/a/path", errMsg: "unknown credential store scheme"},
		"unparseable-url":     {url: "://bad", errMsg: "parse"},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			opened, err := cred.Open(context.Background(), tc.url)
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Same(t, cred.Store(store), opened)
		})
	}
}

func Test_Open_passes_url_and_context_to_opener(t *testing.T) {
	type ctxKey struct{}
	scheme := uniqueScheme("capture")
	var gotURL *url.URL
	var gotCtxValue any
	cred.Register(scheme, func(ctx context.Context, u *url.URL) (cred.Store, error) {
		gotURL = u
		gotCtxValue = ctx.Value(ctxKey{})
		return &fakeStore{}, nil
	})

	ctx := context.WithValue(context.Background(), ctxKey{}, "marker")
	_, err := cred.Open(ctx, scheme+"://host/some/path?opt=1")
	require.NoError(t, err)
	require.Equal(t, "marker", gotCtxValue)
	require.Equal(t, scheme, gotURL.Scheme)
	require.Equal(t, "host", gotURL.Host)
	require.Equal(t, "/some/path", gotURL.Path)
	require.Equal(t, "1", gotURL.Query().Get("opt"))
}
