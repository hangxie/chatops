package mcphost_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/mcphost"
)

func Test_alias_qualifies_names(t *testing.T) {
	host := newHost(t, mcphost.Config{
		Servers: []mcphost.ServerSpec{
			mcphost.InProcess("alpha", "", stubServer("alpha", "one")),
			mcphost.InProcess("beta", "b", stubServer("beta", "one", "two")),
		},
	})

	// The unaliased server keeps its tool names; the aliased one is prefixed,
	// so the two servers' identically named tools coexist.
	require.Equal(t, []string{"b-one", "b-two", "one"}, host.Names())

	result, err := host.CallTool(context.Background(), "b-one", nil)
	require.NoError(t, err)
	require.Equal(t, "beta/one", mcphost.Render(result))

	result, err = host.CallTool(context.Background(), "one", nil)
	require.NoError(t, err)
	require.Equal(t, "alpha/one", mcphost.Render(result))
}

func Test_name_collision_keeps_first(t *testing.T) {
	var logs bytes.Buffer
	host := newHost(t, mcphost.Config{
		Servers: []mcphost.ServerSpec{
			mcphost.InProcess("alpha", "", stubServer("alpha", "shared")),
			mcphost.InProcess("beta", "", stubServer("beta", "shared")),
		},
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})

	require.Equal(t, []string{"shared"}, host.Names())

	// The first server to claim a name keeps it, and the shadowed tool is
	// reported rather than silently taking over.
	result, err := host.CallTool(context.Background(), "shared", nil)
	require.NoError(t, err)
	require.Equal(t, "alpha/shared", mcphost.Render(result))
	require.Contains(t, logs.String(), "tool name already taken")
	require.Contains(t, logs.String(), "server=beta")
	require.Contains(t, logs.String(), "taken_by=alpha")
}

func Test_host_tool_wins_over_server_tool(t *testing.T) {
	var logs bytes.Buffer
	var called boolFlag
	host := newHost(t, mcphost.Config{
		Servers:   []mcphost.ServerSpec{mcphost.InProcess("alpha", "", stubServer("alpha", "reply"))},
		HostTools: []mcphost.HostTool{hostTool("reply", &called.b)},
		Logger:    slog.New(slog.NewTextHandler(&logs, nil)),
	})

	result, err := host.CallTool(context.Background(), "reply", nil)
	require.NoError(t, err)
	require.Equal(t, "host/reply", mcphost.Render(result))
	require.Contains(t, logs.String(), "taken_by=host")
}

func Test_Allow_filters_catalog(t *testing.T) {
	testCases := map[string]struct {
		allow []string
		want  []string
	}{
		"empty-allows-all": {allow: nil, want: []string{"k8s-get", "k8s-list", "ping", "reply"}},
		"exact":            {allow: []string{"ping"}, want: []string{"ping"}},
		"wildcard":         {allow: []string{"k8s-*"}, want: []string{"k8s-get", "k8s-list"}},
		"several":          {allow: []string{"ping", "reply"}, want: []string{"ping", "reply"}},
		"single-char":      {allow: []string{"pin?"}, want: []string{"ping"}},
		"matches-nothing":  {allow: []string{"nope"}, want: []string{}},
		"filters-host-too": {allow: []string{"k8s-get"}, want: []string{"k8s-get"}},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			var called boolFlag
			host := newHost(t, mcphost.Config{
				Servers:   []mcphost.ServerSpec{mcphost.InProcess("alpha", "", stubServer("alpha", "ping", "k8s-get", "k8s-list"))},
				HostTools: []mcphost.HostTool{hostTool("reply", &called.b)},
				Allow:     tc.allow,
			})
			require.Equal(t, tc.want, host.Names())
		})
	}
}

func Test_Allow_hides_tool_from_calls(t *testing.T) {
	host := newHost(t, mcphost.Config{
		Servers: []mcphost.ServerSpec{mcphost.InProcess("alpha", "", stubServer("alpha", "ping", "danger"))},
		Allow:   []string{"ping"},
	})

	// A filtered-out tool is not merely hidden from the catalog: it cannot be
	// called either, so a model that names it anyway gets nowhere.
	_, err := host.CallTool(context.Background(), "danger", nil)
	require.ErrorContains(t, err, `unknown tool "danger"`)
}

func Test_names_are_sanitized_for_function_calling(t *testing.T) {
	// MCP allows tool names that LLM tool-use APIs reject, so characters
	// outside their charset are replaced rather than offered as-is.
	host := newHost(t, mcphost.Config{
		Servers: []mcphost.ServerSpec{mcphost.InProcess("alpha", "", stubServer("alpha", "weird.name", "a/b", "ok-1_2"))},
	})

	require.Equal(t, []string{"a_b", "ok-1_2", "weird_name"}, host.Names())

	result, err := host.CallTool(context.Background(), "weird_name", nil)
	require.NoError(t, err)
	require.Equal(t, "alpha/weird.name", mcphost.Render(result))
}

func Test_alias_is_sanitized_too(t *testing.T) {
	host := newHost(t, mcphost.Config{
		Servers: []mcphost.ServerSpec{mcphost.InProcess("alpha", "my.server", stubServer("alpha", "one"))},
	})

	require.Equal(t, []string{"my_server-one"}, host.Names())
}

func Test_long_names_are_truncated_distinctly(t *testing.T) {
	long := strings.Repeat("a", 70)
	other := strings.Repeat("a", 71)
	host := newHost(t, mcphost.Config{
		Servers: []mcphost.ServerSpec{mcphost.InProcess("alpha", "", stubServer("alpha", long, other))},
	})

	names := host.Names()
	require.Len(t, names, 2)
	for _, name := range names {
		require.LessOrEqual(t, len(name), 64)
	}
	// Truncation keeps a hash of the original, so two long names that share a
	// prefix do not collapse onto each other.
	require.NotEqual(t, names[0], names[1])

	result, err := host.CallTool(context.Background(), names[0], nil)
	require.NoError(t, err)
	require.Contains(t, mcphost.Render(result), "alpha/")
}

func Test_Generation_changes_with_catalog(t *testing.T) {
	host := newHost(t, mcphost.Config{
		Servers: []mcphost.ServerSpec{mcphost.InProcess("alpha", "", stubServer("alpha", "one"))},
	})

	// The generation is stable while the catalog is, so a planner caching its
	// function definitions against it does not rebuild per request.
	first := host.Generation()
	require.Equal(t, first, host.Generation())
	require.NotZero(t, first)
}

// boolFlag wraps an atomic bool so tests can pass its address to hostTool.
type boolFlag struct{ b atomic.Bool }
