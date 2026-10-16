// Package stdio is the interactive stdin/stdout chat backend, for development and local testing only.
package stdio

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hangxie/chatops/internal/config"
	"github.com/hangxie/chatops/internal/conversation"
)

// conversationKey is the one conversation a terminal has; the runner prefixes it with the backend name.
const conversationKey = "stdin"

// replyDrain keeps an accepted message waiting for its reply after cancellation, matching the shutdown grace.
const replyDrain = 30 * time.Second

// Adapter reads lines from in, submits each as a message, and writes the reply to out.
type Adapter struct {
	in     io.Reader
	out    io.Writer
	logger *slog.Logger
	drain  time.Duration

	reading atomic.Bool
}

// New builds a terminal backend reading stdin and writing stdout.
func New(_ config.ChatBackend, logger *slog.Logger) *Adapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Adapter{in: os.Stdin, out: os.Stdout, logger: logger, drain: replyDrain}
}

// Connected reports whether Run is reading input.
func (a *Adapter) Connected() bool { return a.reading.Load() }

// Run answers one message per line until EOF, /quit, /exit, or cancellation, waiting for each reply in turn.
func (a *Adapter) Run(ctx context.Context, submit func(conversation.Message) error) error {
	a.logger.Debug("stdio chat backend started")
	a.reading.Store(true)
	defer a.reading.Store(false)
	lines, ended := a.readLines(ctx)
	for {
		_, _ = fmt.Fprint(a.out, "> ")
		var line string
		select {
		case <-ctx.Done():
			return nil
		case err := <-ended:
			_, _ = fmt.Fprintln(a.out)
			return err
		case line = <-lines:
		}

		input := strings.TrimSpace(line)
		if input == "" {
			continue // a blank line is not a message
		}
		if input == "/quit" || input == "/exit" {
			return nil
		}
		if a.deliver(ctx, submit, input) {
			return nil // the manager is gone or shutdown has begun
		}
	}
}

// deliver submits one message, prints its reply, and reports whether the session should stop.
func (a *Adapter) deliver(ctx context.Context, submit func(conversation.Message) error, input string) (stop bool) {
	replied := make(chan string, 1)
	reply := func(_ context.Context, r conversation.Reply) error { replied <- r.Plain(); return nil }
	err := submit(conversation.Message{Conversation: conversationKey, User: "local", Text: input, Reply: reply})
	switch {
	case err == nil:
	case errors.Is(err, conversation.ErrBusy):
		_, _ = fmt.Fprintln(a.out, "busy; try again shortly")
		return false
	default:
		return true
	}
	select {
	case text := <-replied:
		_, _ = fmt.Fprintln(a.out, text)
		return false
	case <-ctx.Done():
		// Already accepted: wait out the drain for a late reply before stopping, honoring graceful shutdown.
		select {
		case text := <-replied:
			_, _ = fmt.Fprintln(a.out, text)
		case <-time.After(a.drain):
		}
		return true
	}
}

// readLines feeds lines from a goroutine so a blocked read never stalls Run's select; ended carries its result.
// A read already parked in Scan cannot be cancelled, so that goroutine may outlive ctx until the next line or EOF.
func (a *Adapter) readLines(ctx context.Context) (<-chan string, <-chan error) {
	lines := make(chan string)
	ended := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(a.in)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			ended <- fmt.Errorf("read input: %w", err)
			return
		}
		ended <- nil
	}()
	return lines, ended
}
