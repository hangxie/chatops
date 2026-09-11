package builtin_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/builtin"
	"github.com/hangxie/chatops/mcphost"
)

// names opens a host over the given specs and reports the tools it offers.
func names(t *testing.T, specs []mcphost.ServerSpec) []string {
	t.Helper()
	host, err := mcphost.New(context.Background(), mcphost.Config{Servers: specs})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, host.Close()) })
	return host.Names()
}

func Test_Servers(t *testing.T) {
	testCases := map[string]struct {
		selectors   []string
		wantServers int
		want        []string
	}{
		"all-by-default": {
			selectors:   nil,
			wantServers: 3,
			want:        []string{"k8s-get", "k8s-list", "ping", "status-check", "status-list"},
		},
		"one-group":  {selectors: []string{"ping"}, wantServers: 1, want: []string{"ping"}},
		"two-groups": {selectors: []string{"ping", "status"}, wantServers: 2, want: []string{"ping", "status-check", "status-list"}},
		"with-options": {
			selectors:   []string{"k8s?context="},
			wantServers: 1,
			want:        []string{"k8s-get", "k8s-list"},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			specs, err := builtin.Servers(tc.selectors, nil)
			require.NoError(t, err)
			require.Len(t, specs, tc.wantServers)
			// Built-in tool names are distinct across groups, so no group is
			// aliased and the names operators know are preserved.
			require.Equal(t, tc.want, names(t, specs))
		})
	}
}

func Test_Servers_each_group_is_its_own_server(t *testing.T) {
	specs, err := builtin.Servers(nil, nil)
	require.NoError(t, err)

	// One server per group is what makes a single group splittable out later
	// while the rest stay in process.
	require.Len(t, specs, 3)
	seen := map[string]bool{}
	for _, spec := range specs {
		require.NotEmpty(t, spec.Name)
		require.Empty(t, spec.Alias)
		require.NotNil(t, spec.Transport)
		require.NotNil(t, spec.Serve)
		require.False(t, seen[spec.Name])
		seen[spec.Name] = true
	}
	require.Equal(t, map[string]bool{"k8s": true, "ping": true, "status": true}, seen)
}

func Test_Servers_errors(t *testing.T) {
	testCases := map[string]struct {
		selectors []string
		errMsg    string
	}{
		"unknown-group": {selectors: []string{"nope"}, errMsg: "unknown built-in group"},
		"bad-selector":  {selectors: []string{"ping://"}, errMsg: "must be a bare name"},
		"bad-option":    {selectors: []string{"ping?loud=true"}, errMsg: "unknown option"},
		"repeated":      {selectors: []string{"ping", "ping"}, errMsg: `"ping" selected more than once`},
		"repeated-with-option": {
			selectors: []string{"ping", "ping?x=1"},
			errMsg:    `"ping" selected more than once`,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			specs, err := builtin.Servers(tc.selectors, nil)
			require.ErrorContains(t, err, tc.errMsg)
			require.Nil(t, specs)
		})
	}
}
