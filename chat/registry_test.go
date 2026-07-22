package chat_test

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/chat"
	"github.com/hangxie/chatops/cred"
	"github.com/hangxie/chatops/internal/testutils"
)

func fakeOpener(conn chat.Conn, err error) chat.OpenerFunc {
	return func(_ context.Context, _ *url.URL, _ cred.Store) (chat.Conn, error) {
		return conn, err
	}
}

func Test_NewRegistry_invalid_arguments(t *testing.T) {
	opener := fakeOpener(nil, nil)
	testCases := map[string][]chat.Backend{
		"empty-scheme":         {{Scheme: "", Opener: opener}},
		"nil-opener":           {{Scheme: "telnet", Opener: nil}},
		"leading-digit":        {{Scheme: "1abc", Opener: opener}},
		"space":                {{Scheme: "tel net", Opener: opener}},
		"underscore":           {{Scheme: "tel_net", Opener: opener}},
		"colon-and-slash":      {{Scheme: "telnet://", Opener: opener}},
		"non-ascii-letter":     {{Scheme: "sché", Opener: opener}},
		"duplicate":            {{Scheme: "dup", Opener: opener}, {Scheme: "dup", Opener: opener}},
		"duplicate-mixed-case": {{Scheme: "dup", Opener: opener}, {Scheme: "DUP", Opener: opener}},
		"one-good-one-bad":     {{Scheme: "good", Opener: opener}, {Scheme: "", Opener: opener}},
	}

	for name, backends := range testCases {
		t.Run(name, func(t *testing.T) {
			require.Panics(t, func() {
				chat.NewRegistry(backends...)
			})
		})
	}
}

func Test_Registry_Schemes(t *testing.T) {
	opener := fakeOpener(&fakeConn{}, nil)

	testCases := map[string]struct {
		backends []chat.Backend
		want     []string
	}{
		"empty":  {backends: nil, want: []string{}},
		"single": {backends: []chat.Backend{{Scheme: "telnet", Opener: opener}}, want: []string{"telnet"}},
		"sorted": {
			backends: []chat.Backend{
				{Scheme: "telnet", Opener: opener},
				{Scheme: "slack", Opener: opener},
			},
			want: []string{"slack", "telnet"},
		},
		"lowercased": {
			backends: []chat.Backend{{Scheme: "MiXeD", Opener: opener}},
			want:     []string{"mixed"},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			reg := chat.NewRegistry(tc.backends...)
			require.Equal(t, tc.want, reg.Schemes())
		})
	}
}

func Test_Registry_Schemes_returns_independent_copy(t *testing.T) {
	reg := chat.NewRegistry(chat.Backend{Scheme: "telnet", Opener: fakeOpener(&fakeConn{}, nil)})

	got := reg.Schemes()
	got[0] = "mutated"
	require.Equal(t, []string{"telnet"}, reg.Schemes())
}

func Test_NewRegistry_empty_is_valid(t *testing.T) {
	reg := chat.NewRegistry()
	_, err := reg.Open(context.Background(), "telnet://somewhere", nil)
	require.ErrorContains(t, err, `unknown chat backend scheme "telnet"`)
}

func Test_Registry_normalizes_scheme_to_lowercase(t *testing.T) {
	conn := &fakeConn{}
	reg := chat.NewRegistry(chat.Backend{Scheme: "MiXeD", Opener: fakeOpener(conn, nil)})

	// url.Parse lowercases the scheme, so lookup must be lowercase
	// regardless of how the scheme was registered or written in the URL.
	for _, rawURL := range []string{
		"mixed://whatever",
		strings.ToUpper("mixed") + "://whatever",
	} {
		opened, err := reg.Open(context.Background(), rawURL, nil)
		require.NoError(t, err)
		require.Same(t, chat.Conn(conn), opened)
	}
}

func Test_Registry_Open(t *testing.T) {
	conn := &fakeConn{}
	reg := chat.NewRegistry(chat.Backend{Scheme: "fake", Opener: fakeOpener(conn, nil)})

	testCases := map[string]struct {
		url    string
		errMsg string
	}{
		"registered-scheme":   {url: "fake://whatever"},
		"unregistered-scheme": {url: "no-such-backend://whatever", errMsg: `unknown chat backend scheme "no-such-backend"`},
		"no-scheme":           {url: "/just/a/path", errMsg: "unknown chat backend scheme"},
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
			require.Same(t, chat.Conn(conn), opened)
		})
	}
}

func Test_Registry_Open_passes_url_and_context_to_opener(t *testing.T) {
	type ctxKey struct{}
	var gotURL *url.URL
	var gotCtxValue any
	var gotCredentials cred.Store
	credentials := testutils.CredentialStore{}
	reg := chat.NewRegistry(chat.Backend{
		Scheme: "capture",
		Opener: func(ctx context.Context, u *url.URL, creds cred.Store) (chat.Conn, error) {
			gotURL = u
			gotCtxValue = ctx.Value(ctxKey{})
			gotCredentials = creds
			return &fakeConn{}, nil
		},
	})

	ctx := context.WithValue(context.Background(), ctxKey{}, "marker")
	_, err := reg.Open(ctx, "capture://host:1234/some/path?opt=1", credentials)
	require.NoError(t, err)
	require.Equal(t, "marker", gotCtxValue)
	require.Equal(t, "capture", gotURL.Scheme)
	require.Equal(t, "host:1234", gotURL.Host)
	require.Equal(t, "/some/path", gotURL.Path)
	require.Equal(t, "1", gotURL.Query().Get("opt"))
	require.Equal(t, credentials, gotCredentials)
}
