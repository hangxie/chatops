package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/testutils"
)

func Test_Cmd_Run(t *testing.T) {
	tests := map[string]struct {
		cmd   Cmd
		names []string
		// note is what the command says about the selection, on stderr. A
		// selection that excludes no host tool says nothing.
		note string
	}{
		"plain":      {cmd: Cmd{}, names: []string{"reply", "k8s-get", "k8s-list", "ping", "status-check", "status-list"}},
		"json":       {cmd: Cmd{JSON: true}, names: []string{"reply", "k8s-get", "k8s-list", "ping", "status-check", "status-list"}},
		"one-group":  {cmd: Cmd{Builtin: []string{"status"}}, names: []string{"reply", "status-check", "status-list"}},
		"two-groups": {cmd: Cmd{Builtin: []string{"ping", "status"}}, names: []string{"reply", "ping", "status-check", "status-list"}},
		// reply is a host tool and is never filtered, so it heads every
		// listing: a planner is always offered a way to answer.
		"tool-filter": {
			cmd: Cmd{Tools: []string{"k8s-*"}}, names: []string{"reply", "k8s-get", "k8s-list"},
			note: "host tools are offered regardless",
		},
		"tool-filter-2": {
			cmd: Cmd{Tools: []string{"ping"}}, names: []string{"reply", "ping"},
			note: "host tools are offered regardless",
		},
		"tool-filter-covers-reply": {
			cmd: Cmd{Tools: []string{"ping", "reply"}}, names: []string{"reply", "ping"},
		},
		"with-option": {cmd: Cmd{Builtin: []string{"k8s?context=prod"}}, names: []string{"reply", "k8s-get", "k8s-list"}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			stdout, stderr := testutils.CaptureStdoutStderr(func() {
				require.NoError(t, tc.cmd.Run(context.Background()))
			})
			if tc.note == "" {
				require.Empty(t, stderr)
			} else {
				require.Contains(t, stderr, tc.note)
			}

			if tc.cmd.JSON {
				var listings []listing
				require.NoError(t, json.Unmarshal([]byte(stdout), &listings))
				got := make([]string, 0, len(listings))
				for _, l := range listings {
					got = append(got, l.Name)
					// Every offered tool describes itself for the model.
					require.NotEmpty(t, l.Description)
				}
				require.Equal(t, tc.names, got)
				return
			}
			want := ""
			for _, n := range tc.names {
				want += n + "\n"
			}
			require.Equal(t, want, stdout)
		})
	}
}

func Test_Cmd_Run_errors(t *testing.T) {
	testCases := map[string]struct {
		cmd    Cmd
		errMsg string
	}{
		"unknown-group": {cmd: Cmd{Builtin: []string{"nope"}}, errMsg: "unknown built-in group"},
		"bad-selector":  {cmd: Cmd{Builtin: []string{"ping://"}}, errMsg: "must be a bare name"},
		"bad-option":    {cmd: Cmd{Builtin: []string{"ping?loud=true"}}, errMsg: "unknown option"},
		"repeated":      {cmd: Cmd{Builtin: []string{"ping", "ping"}}, errMsg: "selected more than once"},
		"bad-pattern":   {cmd: Cmd{Tools: []string{"[bad"}}, errMsg: `invalid tool pattern "[bad"`},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			require.ErrorContains(t, tc.cmd.Run(context.Background()), tc.errMsg)
		})
	}
}

func Test_unavailable(t *testing.T) {
	// Listing never invokes a tool, so the reply placeholder must refuse.
	_, err := unavailable(context.Background(), nil)
	require.ErrorContains(t, err, "does not invoke tools")
}
