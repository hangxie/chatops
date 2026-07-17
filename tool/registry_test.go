package tool_test

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/cred"
	"github.com/hangxie/chatops/tool"
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

func fakeOpener(tl tool.Tool, err error) tool.OpenerFunc {
	return func(_ context.Context, _ *url.URL, _ cred.Store) (tool.Tool, error) {
		return tl, err
	}
}

func Test_NewRegistry_invalid_arguments(t *testing.T) {
	opener := fakeOpener(nil, nil)
	testCases := map[string][]tool.Backend{
		"empty-scheme":         {{Scheme: "", Opener: opener}},
		"nil-opener":           {{Scheme: "ping", Opener: nil}},
		"leading-digit":        {{Scheme: "1abc", Opener: opener}},
		"space":                {{Scheme: "pi ng", Opener: opener}},
		"underscore":           {{Scheme: "pi_ng", Opener: opener}},
		"colon-and-slash":      {{Scheme: "ping://", Opener: opener}},
		"non-ascii-letter":     {{Scheme: "schémé", Opener: opener}},
		"duplicate":            {{Scheme: "dup", Opener: opener}, {Scheme: "dup", Opener: opener}},
		"duplicate-mixed-case": {{Scheme: "dup", Opener: opener}, {Scheme: "DUP", Opener: opener}},
		"one-good-one-bad":     {{Scheme: "good", Opener: opener}, {Scheme: "", Opener: opener}},
	}

	for name, backends := range testCases {
		t.Run(name, func(t *testing.T) {
			require.Panics(t, func() {
				tool.NewRegistry(backends...)
			})
		})
	}
}

func Test_NewRegistry_empty_is_valid(t *testing.T) {
	reg := tool.NewRegistry()
	_, err := reg.Open(context.Background(), "ping://", nil)
	require.ErrorContains(t, err, `unknown tool scheme "ping"`)
}

func Test_Registry_normalizes_scheme_to_lowercase(t *testing.T) {
	tl := &fakeTool{}
	reg := tool.NewRegistry(tool.Backend{Scheme: "MiXeD", Opener: fakeOpener(tl, nil)})

	// url.Parse lowercases the scheme, so lookup must be lowercase
	// regardless of how the scheme was registered or written in the URL.
	for _, rawURL := range []string{
		"mixed://whatever",
		strings.ToUpper("mixed") + "://whatever",
	} {
		opened, err := reg.Open(context.Background(), rawURL, nil)
		require.NoError(t, err)
		require.Same(t, tool.Tool(tl), opened)
	}
}

func Test_Registry_Open(t *testing.T) {
	tl := &fakeTool{}
	reg := tool.NewRegistry(tool.Backend{Scheme: "fake", Opener: fakeOpener(tl, nil)})

	testCases := map[string]struct {
		url    string
		errMsg string
	}{
		"registered-scheme":   {url: "fake://whatever"},
		"unregistered-scheme": {url: "no-such-tool://whatever", errMsg: `unknown tool scheme "no-such-tool"`},
		"no-scheme":           {url: "/just/a/path", errMsg: "unknown tool scheme"},
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
			require.Same(t, tool.Tool(tl), opened)
		})
	}
}

func Test_Registry_Open_passes_arguments_to_opener(t *testing.T) {
	type ctxKey struct{}
	var gotURL *url.URL
	var gotCtxValue any
	var gotCreds cred.Store
	reg := tool.NewRegistry(tool.Backend{
		Scheme: "capture",
		Opener: func(ctx context.Context, u *url.URL, creds cred.Store) (tool.Tool, error) {
			gotURL = u
			gotCtxValue = ctx.Value(ctxKey{})
			gotCreds = creds
			return &fakeTool{}, nil
		},
	})

	creds := fakeCredStore{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "marker")
	_, err := reg.Open(ctx, "capture://host/some/path?cred-prefix=k8s-prod", creds)
	require.NoError(t, err)
	require.Equal(t, "marker", gotCtxValue)
	require.Equal(t, cred.Store(creds), gotCreds)
	require.Equal(t, "capture", gotURL.Scheme)
	require.Equal(t, "host", gotURL.Host)
	require.Equal(t, "/some/path", gotURL.Path)
	require.Equal(t, "k8s-prod", gotURL.Query().Get("cred-prefix"))
}
