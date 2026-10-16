// Package unix is a chat backend that serves a line-oriented protocol over a Unix domain socket.
package unix

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hangxie/chatops/internal/chat/socket"
	"github.com/hangxie/chatops/internal/config"
	"github.com/hangxie/chatops/internal/conversation"
)

// defaultMode restricts the socket to its owner, since filesystem permissions are its only access control.
const defaultMode = 0o600

// probeTimeout bounds the connect used to tell a live socket from a stale one at startup.
const probeTimeout = 2 * time.Second

// Adapter listens on a Unix domain socket; a local client (nc -U, socat) connects to reach the daemon.
type Adapter struct {
	path   string
	mode   os.FileMode
	logger *slog.Logger
	listen func(string) (net.Listener, error)
	chmod  func(string, os.FileMode) error

	listening atomic.Bool
}

// New builds the backend from the socket path and optional octal mode.
func New(cfg config.ChatBackend, logger *slog.Logger) (*Adapter, error) {
	if logger == nil {
		logger = slog.Default()
	}
	mode := os.FileMode(defaultMode)
	if cfg.Mode != "" {
		parsed, err := strconv.ParseUint(cfg.Mode, 8, 32)
		if err != nil {
			return nil, fmt.Errorf("unix socket mode %q: %w", cfg.Mode, err)
		}
		mode = os.FileMode(parsed)
	}
	return &Adapter{path: cfg.Path, mode: mode, logger: logger, listen: listenUnix, chmod: os.Chmod}, nil
}

// Run creates the socket and serves it until ctx ends. Closing the listener unlinks the socket file (and only
// the one this listener created), so cleanup never removes a socket owned by another process.
func (a *Adapter) Run(ctx context.Context, submit func(conversation.Message) error) error {
	// An exclusive lock, held for the listener's lifetime, serializes the probe/remove/bind sequence so two
	// daemons cannot race on the same path; a second daemon fails fast instead.
	lock, err := acquireLock(a.path + ".lock")
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()

	if err := removeStaleSocket(a.path); err != nil {
		return err
	}
	ln, err := a.listen(a.path)
	if err != nil {
		return fmt.Errorf("listen unix %s: %w", a.path, err)
	}
	defer func() { _ = ln.Close() }()
	if err := a.chmod(a.path, a.mode); err != nil {
		return fmt.Errorf("chmod unix %s: %w", a.path, err)
	}
	a.logger.Info("unix chat backend listening", "path", a.path)
	a.listening.Store(true)
	defer a.listening.Store(false)
	return socket.New(ln, a.logger).Run(ctx, submit)
}

// Connected reports whether the socket is accepting clients.
func (a *Adapter) Connected() bool { return a.listening.Load() }

func listenUnix(path string) (net.Listener, error) { return net.Listen("unix", path) }

// acquireLock takes an exclusive, non-blocking flock on path, held until the returned file is closed.
func acquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, defaultMode)
	if err != nil {
		return nil, fmt.Errorf("open lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("another daemon already holds the lock for %s: %w", path, err)
	}
	return f, nil
}

// removeStaleSocket clears a leftover socket from an earlier run. It never touches a non-socket, and it probes
// the socket first: a live one refuses startup, so a second daemon cannot silently unlink an active endpoint.
func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat socket %s: %w", path, err)
	}
	if info.Mode()&fs.ModeSocket == 0 {
		return fmt.Errorf("path %s exists and is not a socket", path)
	}
	conn, err := net.DialTimeout("unix", path, probeTimeout)
	if err == nil {
		_ = conn.Close()
		return fmt.Errorf("socket %s is already in use by another process", path)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("probe socket %s: %w", path, err)
	}
	return os.Remove(path)
}
