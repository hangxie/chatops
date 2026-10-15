// Package socket serves a line-oriented chat over a net.Listener: one conversation per accepted connection.
package socket

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hangxie/chatops/internal/conversation"
)

// replyDrain keeps an accepted message waiting for its reply after cancellation, matching the shutdown grace.
const replyDrain = 30 * time.Second

// writeTimeout bounds a single reply write so a client that stops reading cannot stall the connection or shutdown.
const writeTimeout = 10 * time.Second

// Server runs the accept loop and per-connection message loop; unix and tcp build a listener and hand it over.
type Server struct {
	listener     net.Listener
	logger       *slog.Logger
	drain        time.Duration
	writeTimeout time.Duration
	seq          atomic.Int64
}

// New wraps listener; the caller owns creating it and closing happens on shutdown.
func New(listener net.Listener, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{listener: listener, logger: logger, drain: replyDrain, writeTimeout: writeTimeout}
}

// Run accepts connections until ctx ends, handling each on its own goroutine, then waits for them to finish.
func (s *Server) Run(ctx context.Context, submit func(conversation.Message) error) error {
	var conns sync.WaitGroup
	context.AfterFunc(ctx, func() { _ = s.listener.Close() })
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			conns.Wait()
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		conns.Add(1)
		go func() {
			defer conns.Done()
			s.handle(ctx, conn, submit)
		}()
	}
}

// handle runs one connection as one conversation until the client disconnects or the server shuts down.
func (s *Server) handle(ctx context.Context, conn net.Conn, submit func(conversation.Message) error) {
	defer func() { _ = conn.Close() }()
	key := fmt.Sprintf("conn-%d", s.seq.Add(1))
	log := s.logger.With("conversation", key, "remote", conn.RemoteAddr())
	// On shutdown, unblock a parked read with a deadline rather than closing the connection, so an in-flight
	// turn can still deliver its reply during the drain window before handle returns and closes it.
	defer context.AfterFunc(ctx, func() { _ = conn.SetReadDeadline(time.Now()) })()

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}
		if s.deliver(ctx, conn, submit, key, input) {
			return
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		log.Debug("connection read ended", "error", err)
	}
}

// deliver submits one message, writes its reply to conn, and reports whether this connection should stop.
func (s *Server) deliver(ctx context.Context, conn net.Conn, submit func(conversation.Message) error, key, input string) (stop bool) {
	replied := make(chan string, 1)
	reply := func(_ context.Context, r conversation.Reply) error { replied <- r.Plain(); return nil }
	err := submit(conversation.Message{Conversation: key, User: key, Text: input, Reply: reply})
	switch {
	case err == nil:
	case errors.Is(err, conversation.ErrBusy):
		s.write(conn, "busy; try again shortly")
		return false
	default:
		return true
	}
	select {
	case text := <-replied:
		return !s.write(conn, text) // stop if the connection is gone
	case <-ctx.Done():
		// Already accepted: wait out the drain for a late reply before stopping, honoring graceful shutdown.
		select {
		case text := <-replied:
			s.write(conn, text)
		case <-time.After(s.drain):
		}
		return true
	}
}

// write sends one reply line under a deadline so a client that stops reading cannot block; it reports success.
func (s *Server) write(conn net.Conn, text string) bool {
	_ = conn.SetWriteDeadline(time.Now().Add(s.writeTimeout))
	_, err := fmt.Fprintln(conn, text)
	return err == nil
}
