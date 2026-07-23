package planner_test

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/cred"
	"github.com/hangxie/chatops/internal/testutils"
	"github.com/hangxie/chatops/planner"
	"github.com/hangxie/chatops/tool"
)

func fakeOpener(p planner.Planner, err error) planner.OpenerFunc {
	return func(_ context.Context, _ *url.URL, _ cred.Store, _ *tool.Registry) (planner.Planner, error) {
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
	require.Empty(t, reg.Schemes())
	_, err := reg.Open(context.Background(), "openai://", nil, nil)
	require.ErrorContains(t, err, `unknown planner scheme "openai"`)
}

func Test_Registry_Schemes_returns_sorted_copy(t *testing.T) {
	reg := planner.NewRegistry(
		planner.Backend{Scheme: "Zulu", Opener: fakeOpener(nil, nil)},
		planner.Backend{Scheme: "alpha", Opener: fakeOpener(nil, nil)},
		planner.Backend{Scheme: "Middle", Opener: fakeOpener(nil, nil)},
	)

	schemes := reg.Schemes()
	require.Equal(t, []string{"alpha", "middle", "zulu"}, schemes)

	schemes[0] = "changed"
	require.Equal(t, []string{"alpha", "middle", "zulu"}, reg.Schemes())
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
		opened, err := reg.Open(context.Background(), rawURL, nil, nil)
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
			opened, err := reg.Open(context.Background(), tc.url, nil, nil)
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
	var gotTools *tool.Registry
	reg := planner.NewRegistry(planner.Backend{
		Scheme: "capture",
		Opener: func(ctx context.Context, u *url.URL, creds cred.Store, tools *tool.Registry) (planner.Planner, error) {
			gotURL = u
			gotCtxValue = ctx.Value(ctxKey{})
			gotCreds = creds
			gotTools = tools
			return &fakePlanner{}, nil
		},
	})

	creds := testutils.CredentialStore{}
	tools := tool.NewRegistry(tool.Backend{
		Scheme:     "widget",
		Opener:     fakeToolOpener,
		Descriptor: &tool.Descriptor{Description: "widget"},
	})
	ctx := context.WithValue(context.Background(), ctxKey{}, "marker")
	_, err := reg.Open(ctx, "capture://host/some/path?model=gpt-5", creds, tools)
	require.NoError(t, err)
	require.Equal(t, "marker", gotCtxValue)
	require.Equal(t, cred.Store(creds), gotCreds)
	require.Same(t, tools, gotTools)
	require.Equal(t, "capture", gotURL.Scheme)
	require.Equal(t, "host", gotURL.Host)
	require.Equal(t, "/some/path", gotURL.Path)
	require.Equal(t, "gpt-5", gotURL.Query().Get("model"))
}

// Test_Registry_Open_nil_tools_becomes_empty verifies a nil tool set is
// normalized to an empty registry so backends never see a nil.
func Test_Registry_Open_nil_tools_becomes_empty(t *testing.T) {
	var gotTools *tool.Registry
	reg := planner.NewRegistry(planner.Backend{
		Scheme: "capture",
		Opener: func(_ context.Context, _ *url.URL, _ cred.Store, tools *tool.Registry) (planner.Planner, error) {
			gotTools = tools
			return &fakePlanner{}, nil
		},
	})

	_, err := reg.Open(context.Background(), "capture://", nil, nil)
	require.NoError(t, err)
	require.NotNil(t, gotTools)
	require.Empty(t, gotTools.Schemes())
}

func fakeToolOpener(_ context.Context, _ *url.URL, _ cred.Store) (tool.Tool, error) {
	return nil, nil
}
