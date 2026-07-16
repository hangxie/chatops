package version

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
			stdout: "v1.2.3\n",
		},
		"plain-with-build": {
			cmd:    Cmd{BuildTime: true},
			stdout: "v1.2.3\ntoday\n",
		},
		"plain-with-source": {
			cmd:    Cmd{Source: true},
			stdout: "v1.2.3\nUT\n",
		},
		"plain-with-all": {
			cmd:    Cmd{All: true},
			stdout: "v1.2.3\ntoday\nUT\n",
		},
		"json": {
			cmd:    Cmd{JSON: true},
			stdout: `{"Version":"v1.2.3"}` + "\n",
		},
		"json-with-build": {
			cmd:    Cmd{JSON: true, BuildTime: true},
			stdout: `{"Version":"v1.2.3","BuildTime":"today"}` + "\n",
		},
		"json-with-source": {
			cmd:    Cmd{JSON: true, Source: true},
			stdout: `{"Version":"v1.2.3","Source":"UT"}` + "\n",
		},
		"json-with-all": {
			cmd:    Cmd{JSON: true, All: true},
			stdout: `{"Version":"v1.2.3","BuildTime":"today","Source":"UT"}` + "\n",
		},
	}

	origVersion, origBuild, origSource := version, build, source
	defer func() { version, build, source = origVersion, origBuild, origSource }()
	version, build, source = "v1.2.3", "today", "UT"

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

func Test_Cmd_Run_devel_fallback(t *testing.T) {
	origVersion := version
	defer func() { version = origVersion }()
	version = ""

	stdout, stderr := testutils.CaptureStdoutStderr(func() {
		require.NoError(t, Cmd{}.Run())
	})
	require.Equal(t, "(devel)\n", stdout)
	require.Empty(t, stderr)
}
