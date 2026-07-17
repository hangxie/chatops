package planner_test

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/cred"
	"github.com/hangxie/chatops/planner"
)

// fakeCredStore is a minimal cred.Store for verifying that the
// registry hands the store through to openers untouched.
type fakeCredStore struct{}

func (fakeCredStore) Get(_ context.Context, key string) (string, error) {
	return "", cred.ErrNotFound
}

func (fakeCredStore) Close() error {
	return nil
}

func fakeOpener(p planner.Planner, err error) planner.OpenerFunc {
	return func(_ context.Context, _ *url.URL, _ cred.Store) (planner.Planner, error) {
		return p, err
	}
}

func Test_NewRegistry_invalid_arguments(t *testing.T) {
	opener := fakeOpener(nil, nil)
	testCases := map[string][]planner.Backend{
		"empty-scheme":         {{Scheme: "", Opener: opener}},
		"nil-opener":           {{Scheme: "openai", Opener: nil}},
		"leading-digit":        {{Scheme: "1abc", Opener: opener}},
		"space":                {{Scheme: "open ai", Opener: opener}},
		"underscore":           {{Scheme: "open_ai", Opener: opener}},
		"colon-and-slash":      {{Scheme: "openai://", Opener: opener}},
		"non-ascii-letter":     {{Scheme: "schémé", Opener: opener}},
		"duplicate":            {{Scheme: "dup", Opener: opener}, {Scheme: "dup", Opener: opener}},
		"duplicate-mixed-case": {{Scheme: "dup", Opener: opener}, {Scheme: "DUP", Opener: opener}},
		"one-good-one-bad":     {{Scheme: "good", Opener: opener}, {Scheme: "", Opener: opener}},
	}

	for name, backends := range testCases {
		t.Run(name, func(t *testing.T) {
			require.Panics(t, func() {
				planner.NewRegistry(backends...)
			})
		})
	}
}

func Test_NewRegistry_empty_is_valid(t *testing.T) {
	reg := planner.NewRegistry()
	_, err := reg.Open(context.Background(), "openai://", nil)
	require.ErrorContains(t, err, `unknown planner scheme "openai"`)
}

func Test_Registry_normalizes_scheme_to_lowercase(t *testing.T) {
	p := &fakePlanner{}
	reg := planner.NewRegistry(planner.Backend{Scheme: "MiXeD", Opener: fakeOpener(p, nil)})

	// url.Parse lowercases the scheme, so lookup must be lowercase
	// regardless of how the scheme was registered or written in the URL.
	for _, rawURL := range []string{
		"mixed://whatever",
		strings.ToUpper("mixed") + "://whatever",
	} {
		opened, err := reg.Open(context.Background(), rawURL, nil)
		require.NoError(t, err)
		require.Same(t, planner.Planner(p), opened)
	}
}

func Test_Registry_Open(t *testing.T) {
	p := &fakePlanner{}
	reg := planner.NewRegistry(planner.Backend{Scheme: "fake", Opener: fakeOpener(p, nil)})

	testCases := map[string]struct {
		url    string
		errMsg string
	}{
		"registered-scheme":   {url: "fake://whatever"},
		"unregistered-scheme": {url: "no-such-planner://whatever", errMsg: `unknown planner scheme "no-such-planner"`},
		"no-scheme":           {url: "/just/a/path", errMsg: "unknown planner scheme"},
		"unparseable-url":     {url: "://bad", errMsg: "parse"},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			opened, err := reg.Open(context.Background(), tc.url, nil)
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Same(t, planner.Planner(p), opened)
		})
	}
}

func Test_Registry_Open_passes_arguments_to_opener(t *testing.T) {
	type ctxKey struct{}
	var gotURL *url.URL
	var gotCtxValue any
	var gotCreds cred.Store
	reg := planner.NewRegistry(planner.Backend{
		Scheme: "capture",
		Opener: func(ctx context.Context, u *url.URL, creds cred.Store) (planner.Planner, error) {
			gotURL = u
			gotCtxValue = ctx.Value(ctxKey{})
			gotCreds = creds
			return &fakePlanner{}, nil
		},
	})

	creds := fakeCredStore{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "marker")
	_, err := reg.Open(ctx, "capture://host/some/path?model=gpt-5", creds)
	require.NoError(t, err)
	require.Equal(t, "marker", gotCtxValue)
	require.Equal(t, cred.Store(creds), gotCreds)
	require.Equal(t, "capture", gotURL.Scheme)
	require.Equal(t, "host", gotURL.Host)
	require.Equal(t, "/some/path", gotURL.Path)
	require.Equal(t, "gpt-5", gotURL.Query().Get("model"))
}
