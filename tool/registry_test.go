package tool_test

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/cred"
	"github.com/hangxie/chatops/internal/testutils"
	"github.com/hangxie/chatops/tool"
)

func fakeOpener(tl tool.Tool, err error) tool.OpenerFunc {
	return func(_ context.Context, _ *url.URL, _ cred.Store) (tool.Tool, error) {
		return tl, err
	}
}

// stubDesc is a minimal valid descriptor for wiring test backends, which
// must self-describe.
func stubDesc() *tool.Descriptor {
	return &tool.Descriptor{Description: "stub"}
}

func Test_NewRegistry_invalid_arguments(t *testing.T) {
	opener := fakeOpener(nil, nil)
	testCases := map[string][]tool.Backend{
		"empty-scheme":     {{Scheme: "", Opener: opener}},
		"nil-opener":       {{Scheme: "ping", Opener: nil}},
		"leading-digit":    {{Scheme: "1abc", Opener: opener}},
		"space":            {{Scheme: "pi ng", Opener: opener}},
		"underscore":       {{Scheme: "pi_ng", Opener: opener}},
		"colon-and-slash":  {{Scheme: "ping://", Opener: opener}},
		"non-ascii-letter": {{Scheme: "schémé", Opener: opener}},
		// Duplicate cases carry descriptors so the duplicate check is
		// reached before the required-descriptor check.
		"duplicate":            {{Scheme: "dup", Opener: opener, Descriptor: stubDesc()}, {Scheme: "dup", Opener: opener, Descriptor: stubDesc()}},
		"duplicate-mixed-case": {{Scheme: "dup", Opener: opener, Descriptor: stubDesc()}, {Scheme: "DUP", Opener: opener, Descriptor: stubDesc()}},
		"one-good-one-bad":     {{Scheme: "good", Opener: opener, Descriptor: stubDesc()}, {Scheme: "", Opener: opener}},
		"nil-descriptor":       {{Scheme: "ping", Opener: opener}},
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
	require.Empty(t, reg.Schemes())
	_, err := reg.Open(context.Background(), "ping://", nil)
	require.ErrorContains(t, err, `unknown tool scheme "ping"`)
}

func Test_Registry_Schemes_returns_sorted_copy(t *testing.T) {
	reg := tool.NewRegistry(
		tool.Backend{Scheme: "Zulu", Opener: fakeOpener(nil, nil), Descriptor: stubDesc()},
		tool.Backend{Scheme: "alpha", Opener: fakeOpener(nil, nil), Descriptor: stubDesc()},
		tool.Backend{Scheme: "Middle", Opener: fakeOpener(nil, nil), Descriptor: stubDesc()},
	)

	schemes := reg.Schemes()
	require.Equal(t, []string{"alpha", "middle", "zulu"}, schemes)

	schemes[0] = "changed"
	require.Equal(t, []string{"alpha", "middle", "zulu"}, reg.Schemes())
}

func Test_Registry_Select(t *testing.T) {
	reg := tool.NewRegistry(
		tool.Backend{Scheme: "ping", Opener: fakeOpener(&fakeTool{}, nil), Descriptor: stubDesc()},
		tool.Backend{Scheme: "status", Opener: fakeOpener(&fakeTool{}, nil), Descriptor: stubDesc()},
	)
	testCases := map[string]struct {
		selected []string
		want     []string
		errMsg   string
	}{
		"filtered":     {selected: []string{"status"}, want: []string{"status"}},
		"repeated":     {selected: []string{"status", "ping"}, want: []string{"ping", "status"}},
		"duplicate":    {selected: []string{"ping", "ping"}, want: []string{"ping"}},
		"mixed-case":   {selected: []string{"PING"}, want: []string{"ping"}},
		"url-form":     {selected: []string{"status://?region=us-west"}, want: []string{"status"}},
		"url-and-name": {selected: []string{"status://?x=1", "ping"}, want: []string{"ping", "status"}},
		"invalid": {
			selected: []string{"ping", "bogus"},
			errMsg:   `tool: unknown tool "bogus"; available tools: ping, status`,
		},
		"invalid-url-scheme": {
			selected: []string{"status://ok", "nope://x"},
			errMsg:   `tool: unknown tool "nope"; available tools: ping, status`,
		},
		"unparseable": {
			selected: []string{"://bad"},
			errMsg:   "tool: parse tool selector",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			selected, err := reg.Select(tc.selected...)
			if tc.errMsg != "" {
				require.Nil(t, selected)
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, selected.Schemes())
		})
	}
}

func Test_Registry_Select_configures_tool_url(t *testing.T) {
	var gotURL *url.URL
	reg := tool.NewRegistry(tool.Backend{
		Scheme: "capture",
		Opener: func(_ context.Context, u *url.URL, _ cred.Store) (tool.Tool, error) {
			gotURL = u
			return &fakeTool{}, nil
		},
		Descriptor: stubDesc(),
	})

	t.Run("url form supplies operator config to the opener", func(t *testing.T) {
		gotURL = nil
		selected, err := reg.Select("capture://?context=chatops&namespace=web")
		require.NoError(t, err)

		// The planner emits a bare scheme URL; the operator-configured URL
		// replaces it for that scheme.
		_, err = selected.Open(context.Background(), "capture://", nil)
		require.NoError(t, err)
		require.Equal(t, "chatops", gotURL.Query().Get("context"))
		require.Equal(t, "web", gotURL.Query().Get("namespace"))
	})

	t.Run("bare name leaves the caller URL untouched", func(t *testing.T) {
		gotURL = nil
		selected, err := reg.Select("capture")
		require.NoError(t, err)

		_, err = selected.Open(context.Background(), "capture://host/path?region=us-west", nil)
		require.NoError(t, err)
		require.Equal(t, "host", gotURL.Host)
		require.Equal(t, "us-west", gotURL.Query().Get("region"))
	})
}

func Test_Registry_Configure(t *testing.T) {
	var gotURL *url.URL
	reg := tool.NewRegistry(
		tool.Backend{
			Scheme: "capture",
			Opener: func(_ context.Context, u *url.URL, _ cred.Store) (tool.Tool, error) {
				gotURL = u
				return &fakeTool{}, nil
			},
			Descriptor: stubDesc(),
		},
		tool.Backend{Scheme: "other", Opener: fakeOpener(&fakeTool{}, nil), Descriptor: stubDesc()},
	)

	t.Run("configures a tool without restricting the exposed set", func(t *testing.T) {
		gotURL = nil
		configured, err := reg.Configure("capture://?context=prod")
		require.NoError(t, err)

		// Both tools remain exposed; only capture gained configuration.
		require.Equal(t, []string{"capture", "other"}, configured.Schemes())
		_, err = configured.Open(context.Background(), "capture://", nil)
		require.NoError(t, err)
		require.Equal(t, "prod", gotURL.Query().Get("context"))
		_, err = configured.Open(context.Background(), "other://", nil)
		require.NoError(t, err)
	})

	t.Run("later URL overrides an earlier one for the same tool", func(t *testing.T) {
		gotURL = nil
		configured, err := reg.Configure("capture://?context=a", "capture://?context=b")
		require.NoError(t, err)
		_, err = configured.Open(context.Background(), "capture://", nil)
		require.NoError(t, err)
		require.Equal(t, "b", gotURL.Query().Get("context"))
	})

	t.Run("does not mutate the source registry", func(t *testing.T) {
		gotURL = nil
		_, err := reg.Configure("capture://?context=prod")
		require.NoError(t, err)
		_, err = reg.Open(context.Background(), "capture://", nil)
		require.NoError(t, err)
		require.Empty(t, gotURL.Query().Get("context"))
	})

	testCases := map[string]struct {
		args   []string
		errMsg string
	}{
		"bare name":        {args: []string{"capture"}, errMsg: `tool: "capture" is not a tool URL`},
		"unexposed scheme": {args: []string{"nope://?x=1"}, errMsg: `tool: cannot configure unexposed tool "nope"; available tools: capture, other`},
		"unparseable":      {args: []string{"://bad"}, errMsg: "tool: parse tool selector"},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			configured, err := reg.Configure(tc.args...)
			require.Nil(t, configured)
			require.ErrorContains(t, err, tc.errMsg)
		})
	}
}

func Test_Registry_Select_then_Configure(t *testing.T) {
	reg := tool.NewRegistry(
		tool.Backend{Scheme: "ping", Opener: fakeOpener(&fakeTool{}, nil), Descriptor: stubDesc()},
		tool.Backend{Scheme: "status", Opener: fakeOpener(&fakeTool{}, nil), Descriptor: stubDesc()},
	)

	selected, err := reg.Select("status")
	require.NoError(t, err)

	// Configuring a tool the allowlist excludes is rejected.
	_, err = selected.Configure("ping://?x=1")
	require.ErrorContains(t, err, `cannot configure unexposed tool "ping"`)

	// Configuring an exposed tool preserves the allowlist.
	configured, err := selected.Configure("status://?x=1")
	require.NoError(t, err)
	require.Equal(t, []string{"status"}, configured.Schemes())
}

func Test_Registry_normalizes_scheme_to_lowercase(t *testing.T) {
	tl := &fakeTool{}
	reg := tool.NewRegistry(tool.Backend{Scheme: "MiXeD", Opener: fakeOpener(tl, nil), Descriptor: stubDesc()})

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
	reg := tool.NewRegistry(tool.Backend{Scheme: "fake", Opener: fakeOpener(tl, nil), Descriptor: stubDesc()})

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
		Descriptor: stubDesc(),
	})

	creds := testutils.CredentialStore{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "marker")
	_, err := reg.Open(ctx, "capture://host/some/path?region=us-west", creds)
	require.NoError(t, err)
	require.Equal(t, "marker", gotCtxValue)
	require.Equal(t, cred.Store(creds), gotCreds)
	require.Equal(t, "capture", gotURL.Scheme)
	require.Equal(t, "host", gotURL.Host)
	require.Equal(t, "/some/path", gotURL.Path)
	require.Equal(t, "us-west", gotURL.Query().Get("region"))
}
