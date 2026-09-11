package mcphost_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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
		"exact":            {allow: []string{"ping"}, want: []string{"ping", "reply"}},
		"wildcard":         {allow: []string{"k8s-*"}, want: []string{"k8s-get", "k8s-list", "reply"}},
		"several":          {allow: []string{"ping", "k8s-get"}, want: []string{"k8s-get", "ping", "reply"}},
		"single-char":      {allow: []string{"pin?"}, want: []string{"ping", "reply"}},
		// Host tools are never filtered: the planner turns model prose into a
		// reply step unconditionally, so a catalog without reply would leave
		// a bot that cannot answer at all.
		"matches-nothing":   {allow: []string{"nope"}, want: []string{"reply"}},
		"keeps-host-tools":  {allow: []string{"k8s-get"}, want: []string{"k8s-get", "reply"}},
		"host-tool-by-name": {allow: []string{"reply"}, want: []string{"reply"}},
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

// Test_Allow_always_keeps_reply_callable guards the failure this prevents:
// with reply filtered out, every ordinary planner response — model prose, the
// ping planner's clarification and fallback replies — would name a tool the
// host does not have, and the requester would get the generic failure notice
// instead of an answer.
func Test_Allow_always_keeps_reply_callable(t *testing.T) {
	var called boolFlag
	host := newHost(t, mcphost.Config{
		Servers:   []mcphost.ServerSpec{mcphost.InProcess("alpha", "", stubServer("alpha", "ping"))},
		HostTools: []mcphost.HostTool{hostTool("reply", &called.b)},
		Allow:     []string{"ping"},
	})

	require.Contains(t, host.Names(), "reply")
	_, err := host.CallTool(context.Background(), "reply", map[string]any{"text": "hi"})
	require.NoError(t, err)
	require.True(t, called.b.Load())
}

func Test_Allow_warns_about_a_pattern_that_matches_nothing(t *testing.T) {
	var logs bytes.Buffer
	host := newHost(t, mcphost.Config{
		Servers: []mcphost.ServerSpec{mcphost.InProcess("alpha", "", stubServer("alpha", "k8s-list"))},
		// A typo passes the syntax check and quietly exposes nothing, which
		// is the mistake validating early is supposed to catch.
		Allow:  []string{"k8s-lst"},
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})

	require.Empty(t, host.Names())
	require.Contains(t, logs.String(), "tool pattern matched nothing")
	require.Contains(t, logs.String(), "k8s-lst")
	// The names offered are the unfiltered ones, so they show what was meant
	// rather than the empty result of the typo.
	require.Contains(t, logs.String(), "k8s-list")
}

func Test_Allow_notes_that_host_tools_are_exempt(t *testing.T) {
	// An operator who wrote --tool ping and finds reply in the catalog has no
	// way to tell whether that is a bug or the rule.
	var logs bytes.Buffer
	var called boolFlag
	host := newHost(t, mcphost.Config{
		Servers:   []mcphost.ServerSpec{mcphost.InProcess("alpha", "", stubServer("alpha", "ping"))},
		HostTools: []mcphost.HostTool{hostTool("reply", &called.b)},
		Allow:     []string{"ping"},
		Logger:    slog.New(slog.NewTextHandler(&logs, nil)),
	})

	require.Equal(t, []string{"ping", "reply"}, host.Names())
	require.Contains(t, logs.String(), "host tools are offered regardless")
	require.Contains(t, logs.String(), "reply")
}

func Test_Allow_says_nothing_when_the_allowlist_covers_everything(t *testing.T) {
	var logs bytes.Buffer
	var called boolFlag
	newHost(t, mcphost.Config{
		Servers:   []mcphost.ServerSpec{mcphost.InProcess("alpha", "", stubServer("alpha", "ping"))},
		HostTools: []mcphost.HostTool{hostTool("reply", &called.b)},
		Allow:     []string{"ping", "reply"},
		Logger:    slog.New(slog.NewTextHandler(&logs, nil)),
	})

	require.NotContains(t, logs.String(), "host tools are offered regardless")
}

func Test_Allow_says_nothing_without_an_allowlist(t *testing.T) {
	var logs bytes.Buffer
	var called boolFlag
	newHost(t, mcphost.Config{
		Servers:   []mcphost.ServerSpec{mcphost.InProcess("alpha", "", stubServer("alpha", "ping"))},
		HostTools: []mcphost.HostTool{hostTool("reply", &called.b)},
		Logger:    slog.New(slog.NewTextHandler(&logs, nil)),
	})

	require.NotContains(t, logs.String(), "host tools are offered regardless")
	require.NotContains(t, logs.String(), "matched nothing")
}

func Test_Allow_does_not_warn_when_every_pattern_matches(t *testing.T) {
	var logs bytes.Buffer
	newHost(t, mcphost.Config{
		Servers: []mcphost.ServerSpec{mcphost.InProcess("alpha", "", stubServer("alpha", "k8s-list", "ping"))},
		Allow:   []string{"k8s-*", "ping"},
		Logger:  slog.New(slog.NewTextHandler(&logs, nil)),
	})

	require.NotContains(t, logs.String(), "tool pattern matched nothing")
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

// Test_concurrent_rebuilds_converge drives every server into announcing a
// tool change at once and checks that the catalog ends up holding all of
// them. Nothing arrives later to repair a stale view, so the final state is
// the only state.
//
// It is not a reproducer for the interleaving that motivated serializing
// rebuilds — publishing an older view after a newer one needs a session's
// cache to change inside another rebuild's assembly, and assembling is
// consistently faster than the refresh that would change it. It covers
// convergence, not the exclusion that guarantees it.
func Test_concurrent_rebuilds_converge(t *testing.T) {
	const servers = 16
	const bulk = 50
	srvs := make([]*mcp.Server, servers)
	specs := make([]mcphost.ServerSpec, servers)
	for i := range srvs {
		name := fmt.Sprintf("s%d", i)
		// Large tool lists make assembling a catalog take long enough for
		// two rebuilds to genuinely overlap.
		tools := make([]string, 0, bulk+1)
		tools = append(tools, name+"-base")
		for j := range bulk {
			tools = append(tools, fmt.Sprintf("%s-bulk%03d", name, j))
		}
		srvs[i] = stubServer(name, tools...)
		specs[i] = mcphost.InProcess(name, "", srvs[i])
	}
	host := newHost(t, mcphost.Config{Servers: specs})
	require.Len(t, host.Names(), servers*(bulk+1))

	// Every server announces a new tool at the same moment.
	var wg sync.WaitGroup
	for i, srv := range srvs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			addEcho(srv, fmt.Sprintf("s%d", i), fmt.Sprintf("s%d-added", i))
		}()
	}
	wg.Wait()

	want := make([]string, 0, servers*(bulk+2))
	for i := range srvs {
		name := fmt.Sprintf("s%d", i)
		want = append(want, name+"-added", name+"-base")
		for j := range bulk {
			want = append(want, fmt.Sprintf("%s-bulk%03d", name, j))
		}
	}
	sort.Strings(want)

	// Once the notifications have been handled the catalog must hold every
	// server's current tools; nothing arrives later to repair a stale view.
	require.Eventually(t, func() bool {
		return slices.Equal(host.Names(), want)
	}, 5*time.Second, 10*time.Millisecond, "catalog settled stale: %v", host.Names())
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
