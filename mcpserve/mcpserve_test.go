package mcpserve_test

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/testutils"
	"github.com/hangxie/chatops/mcpserve"
)

// registerEcho is a RegisterFunc adding one tool that reports the options the
// group was built with, so tests can see configuration reach the group.
func registerEcho(s *mcp.Server, opts mcpserve.Options) error {
	mcp.AddTool(s, &mcp.Tool{Name: "echo", Description: "Report the group options."},
		func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: opts.Query.Encode()}}}, nil, nil
		})
	return nil
}

func Test_NewRegistry_names(t *testing.T) {
	reg := mcpserve.NewRegistry(
		mcpserve.Group{Name: "zulu", Register: registerEcho},
		mcpserve.Group{Name: "alpha", Register: registerEcho},
	)
	require.Equal(t, []string{"alpha", "zulu"}, reg.Names())

	// The returned slice is a copy.
	names := reg.Names()
	names[0] = "mutated"
	require.Equal(t, []string{"alpha", "zulu"}, reg.Names())
}

func Test_NewRegistry_panics_on_bad_wiring(t *testing.T) {
	testCases := map[string]struct {
		groups []mcpserve.Group
		panics string
	}{
		"empty-name":   {groups: []mcpserve.Group{{Name: "", Register: registerEcho}}, panics: "invalid group name"},
		"upper-case":   {groups: []mcpserve.Group{{Name: "K8s", Register: registerEcho}}, panics: "invalid group name"},
		"leading-dash": {groups: []mcpserve.Group{{Name: "-k8s", Register: registerEcho}}, panics: "invalid group name"},
		"nil-register": {groups: []mcpserve.Group{{Name: "k8s"}}, panics: "nil register function"},
		"duplicate": {
			groups: []mcpserve.Group{{Name: "k8s", Register: registerEcho}, {Name: "k8s", Register: registerEcho}},
			panics: "duplicate group",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			require.PanicsWithValue(t, panicMessage(tc.panics, tc.groups), func() {
				mcpserve.NewRegistry(tc.groups...)
			})
		})
	}
}

// panicMessage reconstructs the panic text NewRegistry produces, so the test
// asserts the whole message rather than a fragment of it.
func panicMessage(kind string, groups []mcpserve.Group) string {
	switch kind {
	case "invalid group name":
		return `mcpserve: NewRegistry with invalid group name "` + groups[0].Name + `"`
	case "nil register function":
		return `mcpserve: NewRegistry with nil register function for group "` + groups[0].Name + `"`
	default:
		return `mcpserve: NewRegistry with duplicate group "` + groups[1].Name + `"`
	}
}

func Test_Server(t *testing.T) {
	reg := mcpserve.NewRegistry(mcpserve.Group{Name: "echo", Register: registerEcho})

	srv, err := reg.Server("echo", mcpserve.Options{Query: url.Values{"context": {"prod"}}})
	require.NoError(t, err)
	session := testutils.MCPSession(t, srv)

	require.Equal(t, []string{"echo"}, testutils.ToolNames(t, session))

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "echo"})
	require.NoError(t, err)
	require.Equal(t, "context=prod", testutils.ResultText(t, result))
}

func Test_Server_gives_a_group_a_usable_logger(t *testing.T) {
	// A group has nowhere else to put what must stay out of a tool result,
	// so its logger is always usable even when the caller supplies none.
	var got *slog.Logger
	reg := mcpserve.NewRegistry(mcpserve.Group{Name: "probe", Register: func(_ *mcp.Server, opts mcpserve.Options) error {
		got = opts.Logger
		return nil
	}})

	_, err := reg.Server("probe", mcpserve.Options{})
	require.NoError(t, err)
	require.NotNil(t, got)

	supplied := slog.New(slog.DiscardHandler)
	_, err = reg.Server("probe", mcpserve.Options{Logger: supplied})
	require.NoError(t, err)
	require.Same(t, supplied, got)
}

func Test_Server_nil_query(t *testing.T) {
	reg := mcpserve.NewRegistry(mcpserve.Group{Name: "echo", Register: registerEcho})

	// A nil query reaches the group as an empty one, so groups never need a
	// nil check.
	srv, err := reg.Server("echo", mcpserve.Options{})
	require.NoError(t, err)
	session := testutils.MCPSession(t, srv)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "echo"})
	require.NoError(t, err)
	require.Empty(t, testutils.ResultText(t, result))
}

func Test_Server_errors(t *testing.T) {
	failing := func(*mcp.Server, mcpserve.Options) error { return errors.New("boom") }
	reg := mcpserve.NewRegistry(
		mcpserve.Group{Name: "echo", Register: registerEcho},
		mcpserve.Group{Name: "broken", Register: failing},
	)

	_, err := reg.Server("missing", mcpserve.Options{})
	require.ErrorContains(t, err, `unknown built-in group "missing"`)
	require.ErrorContains(t, err, "available groups: broken, echo")

	_, err = reg.Server("broken", mcpserve.Options{})
	require.ErrorContains(t, err, `build built-in group "broken": boom`)
}

func Test_ParseSpec(t *testing.T) {
	testCases := map[string]struct {
		spec      string
		wantName  string
		wantQuery url.Values
		errMsg    string
	}{
		"bare":        {spec: "k8s", wantName: "k8s", wantQuery: url.Values{}},
		"with-option": {spec: "k8s?context=prod", wantName: "k8s", wantQuery: url.Values{"context": {"prod"}}},
		"two-options": {
			spec:      "k8s?context=prod&kubeconfig=/tmp/kc",
			wantName:  "k8s",
			wantQuery: url.Values{"context": {"prod"}, "kubeconfig": {"/tmp/kc"}},
		},
		"scheme":      {spec: "k8s://", errMsg: "must be a bare name"},
		"host":        {spec: "//host/k8s", errMsg: "must be a bare name"},
		"fragment":    {spec: "k8s#frag", errMsg: "must be a bare name"},
		"empty":       {spec: "", errMsg: "invalid built-in group name"},
		"unparseable": {spec: "%zz", errMsg: "parse built-in group"},
		"uppercase":   {spec: "K8S", errMsg: "invalid built-in group name"},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotName, gotQuery, err := mcpserve.ParseSpec(tc.spec)
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantName, gotName)
			require.Equal(t, tc.wantQuery, gotQuery)
		})
	}
}

func Test_CheckOptions(t *testing.T) {
	testCases := map[string]struct {
		opts    url.Values
		allowed []string
		errMsg  string
	}{
		"none":             {opts: nil, allowed: []string{"context"}},
		"permitted":        {opts: url.Values{"context": {"prod"}}, allowed: []string{"context"}},
		"unknown":          {opts: url.Values{"cluster": {"prod"}}, allowed: []string{"context"}, errMsg: "unknown option cluster; supported options: context"},
		"unknown-sorted":   {opts: url.Values{"b": {"1"}, "a": {"1"}}, allowed: []string{"context"}, errMsg: "unknown option a, b"},
		"group-takes-none": {opts: url.Values{"x": {"1"}}, errMsg: "this group takes no options"},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			err := mcpserve.CheckOptions("grp", tc.opts, tc.allowed...)
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				require.ErrorContains(t, err, "grp:")
				return
			}
			require.NoError(t, err)
		})
	}
}
