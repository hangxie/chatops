package unix

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/config"
	"github.com/hangxie/chatops/internal/conversation"
)

func echoSubmit(msg conversation.Message) error {
	return msg.Reply(context.Background(), "pong:"+msg.Text)
}

// socketDir returns a short-named temp dir; a long t.TempDir() path overflows the Unix sun_path limit.
func socketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cs")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func Test_New(t *testing.T) {
	a, err := New(config.ChatBackend{Type: config.ChatUnix, Path: "/run/x.sock"}, nil)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), a.mode) // owner-only default

	a, err = New(config.ChatBackend{Type: config.ChatUnix, Path: "/run/x.sock", Mode: "0660"}, nil)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o660), a.mode)

	_, err = New(config.ChatBackend{Type: config.ChatUnix, Path: "/run/x.sock", Mode: "nope"}, nil)
	require.ErrorContains(t, err, "unix socket mode")
}

func Test_Run(t *testing.T) {
	path := filepath.Join(socketDir(t), "s.sock")
	a, err := New(config.ChatBackend{Type: config.ChatUnix, Path: path, Mode: "0600"}, nil)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, echoSubmit) }()

	conn := dialWhenReady(t, path)
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
	_, err = conn.Write([]byte("hi\n"))
	require.NoError(t, err)
	line, err := bufio.NewReader(conn).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "pong:hi\n", line)

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	cancel()
	require.NoError(t, <-done)
	_, err = os.Stat(path)
	require.True(t, os.IsNotExist(err), "socket file is removed on exit")
}

func Test_Run_listen_error(t *testing.T) {
	// Bind failure is reached only after the lock and stale checks succeed, so use a real path.
	path := filepath.Join(socketDir(t), "s.sock")
	a, err := New(config.ChatBackend{Type: config.ChatUnix, Path: path}, nil)
	require.NoError(t, err)
	a.listen = func(string) (net.Listener, error) { return nil, errors.New("bind refused") }
	require.ErrorContains(t, a.Run(context.Background(), echoSubmit), "listen unix")
}

func Test_Run_lock_error(t *testing.T) {
	a, err := New(config.ChatBackend{Type: config.ChatUnix, Path: "/no/such/dir/x.sock"}, nil)
	require.NoError(t, err)
	require.ErrorContains(t, a.Run(context.Background(), echoSubmit), "open lock")
}

func Test_acquireLock(t *testing.T) {
	path := filepath.Join(socketDir(t), "x.lock")
	f, err := acquireLock(path)
	require.NoError(t, err)
	_, err = acquireLock(path)
	require.ErrorContains(t, err, "already holds the lock") // a second holder is refused
	require.NoError(t, f.Close())
	again, err := acquireLock(path) // the lock is free once released
	require.NoError(t, err)
	require.NoError(t, again.Close())

	_, err = acquireLock("/no/such/dir/x.lock")
	require.ErrorContains(t, err, "open lock")
}

func Test_Run_chmod_error(t *testing.T) {
	path := filepath.Join(socketDir(t), "s.sock")
	a, err := New(config.ChatBackend{Type: config.ChatUnix, Path: path}, nil)
	require.NoError(t, err)
	a.chmod = func(string, os.FileMode) error { return os.ErrPermission }
	require.ErrorContains(t, a.Run(context.Background(), echoSubmit), "chmod unix")
	_, err = os.Stat(path)
	require.True(t, os.IsNotExist(err), "a failed chmod still removes the socket file")
}

func Test_Run_path_not_a_socket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "regular")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	a, err := New(config.ChatBackend{Type: config.ChatUnix, Path: path}, nil)
	require.NoError(t, err)
	require.ErrorContains(t, a.Run(context.Background(), echoSubmit), "not a socket")
}

func Test_removeStaleSocket(t *testing.T) {
	dir := socketDir(t)

	require.NoError(t, removeStaleSocket(filepath.Join(dir, "missing.sock"))) // nothing to remove

	leftover := filepath.Join(dir, "stale.sock")
	ln, err := net.Listen("unix", leftover)
	require.NoError(t, err)
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	require.NoError(t, ln.Close()) // the socket file stays behind
	require.NoError(t, removeStaleSocket(leftover))
	_, err = os.Stat(leftover)
	require.True(t, os.IsNotExist(err))

	regular := filepath.Join(dir, "file")
	require.NoError(t, os.WriteFile(regular, nil, 0o600))
	require.ErrorContains(t, removeStaleSocket(regular), "not a socket")

	// A stat error other than "not found" (here ENOTDIR: a path under a regular file) is surfaced, not ignored.
	require.ErrorContains(t, removeStaleSocket(filepath.Join(regular, "child.sock")), "stat socket")
}

func Test_removeStaleSocket_active_refused(t *testing.T) {
	path := filepath.Join(socketDir(t), "l.sock")
	ln, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	require.ErrorContains(t, removeStaleSocket(path), "already in use") // a live socket is never unlinked
	_, err = os.Stat(path)
	require.NoError(t, err, "the active socket is left intact")
}

func dialWhenReady(t *testing.T, path string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.Dial("unix", path)
		if err == nil {
			t.Cleanup(func() { _ = conn.Close() })
			return conn
		}
		if time.Now().After(deadline) {
			t.Fatalf("socket never became dialable: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
