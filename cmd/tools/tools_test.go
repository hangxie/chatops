package tools

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/testutils"
)

func Test_Cmd_Run(t *testing.T) {
	tests := map[string]struct {
		cmd    Cmd
		stdout string
	}{
		"plain": {
			cmd:    Cmd{},
			stdout: "k8s-get\nk8s-list\nping\nstatus-check\nstatus-list\n",
		},
		"json": {
			cmd:    Cmd{JSON: true},
			stdout: `["k8s-get","k8s-list","ping","status-check","status-list"]` + "\n",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			stdout, stderr := testutils.CaptureStdoutStderr(func() {
				require.NoError(t, tc.cmd.Run())
			})
			require.Equal(t, tc.stdout, stdout)
			require.Empty(t, stderr)
		})
	}
}
