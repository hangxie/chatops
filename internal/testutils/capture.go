// Package testutils provides helpers shared by command tests.
package testutils

import (
	"io"
	"os"
	"sync"
)

var stdCaptureMutex sync.Mutex

// CaptureStdoutStderr runs f and returns everything it wrote to os.Stdout
// and os.Stderr. A mutex serializes captures so parallel tests do not
// interleave output.
func CaptureStdoutStderr(f func()) (string, string) {
	stdCaptureMutex.Lock()
	defer stdCaptureMutex.Unlock()

	savedStdout := os.Stdout
	savedStderr := os.Stderr

	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout = wOut
	os.Stderr = wErr
	f()
	_ = wOut.Close()
	_ = wErr.Close()
	stdout, _ := io.ReadAll(rOut)
	stderr, _ := io.ReadAll(rErr)
	_ = rOut.Close()
	_ = rErr.Close()

	os.Stdout = savedStdout
	os.Stderr = savedStderr

	return string(stdout), string(stderr)
}
