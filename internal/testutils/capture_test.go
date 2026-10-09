package testutils

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_CaptureStdoutStderr(t *testing.T) {
	tests := map[string]struct {
		f      func()
		stdout string
		stderr string
	}{
		"stdout-only": {
			f:      func() { fmt.Println("out") },
			stdout: "out\n",
			stderr: "",
		},
		"stderr-only": {
			f:      func() { fmt.Fprintln(os.Stderr, "err") },
			stdout: "",
			stderr: "err\n",
		},
		"both": {
			f: func() {
				fmt.Println("out")
				fmt.Fprintln(os.Stderr, "err")
			},
			stdout: "out\n",
			stderr: "err\n",
		},
		"neither": {
			f:      func() {},
			stdout: "",
			stderr: "",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			stdout, stderr := CaptureStdoutStderr(tc.f)
			require.Equal(t, tc.stdout, stdout)
			require.Equal(t, tc.stderr, stderr)
		})
	}
}

func Test_CaptureStdoutStderr_restores(t *testing.T) {
	savedStdout, savedStderr := os.Stdout, os.Stderr
	_, _ = CaptureStdoutStderr(func() {})
	require.Equal(t, savedStdout, os.Stdout)
	require.Equal(t, savedStderr, os.Stderr)
}
