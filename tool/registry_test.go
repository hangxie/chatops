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
	return &tool.Descriptor{Summary: "stub", Actions: []tool.Action{{Name: "do"}}}
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
		"filtered":   {selected: []string{"status"}, want: []string{"status"}},
		"repeated":   {selected: []string{"status", "ping"}, want: []string{"ping", "status"}},
		"duplicate":  {selected: []string{"ping", "ping"}, want: []string{"ping"}},
		"mixed-case": {selected: []string{"PING"}, want: []string{"ping"}},
		"invalid": {
			selected: []string{"ping", "bogus"},
			errMsg:   `tool: unknown tool "bogus"; available tools: ping, status`,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			selected, err := reg.Select(tc.selected...)
			if tc.errMsg != "" {
				require.Nil(t, selected)
				require.EqualError(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, selected.Schemes())
		})
	}
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
