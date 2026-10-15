package stdio

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/config"
	"github.com/hangxie/chatops/internal/conversation"
)

func newTest(in string) (*Adapter, *bytes.Buffer) {
	out := &bytes.Buffer{}
	return &Adapter{in: strings.NewReader(in), out: out, logger: slog.Default(), drain: 50 * time.Millisecond}, out
}

// echo acts like the manager: it answers every message by invoking its Reply closure.
func echo(_ context.Context, msg conversation.Message) {
	_ = msg.Reply(context.Background(), conversation.Reply{Text: "pong:" + msg.Text})
}

func Test_New(t *testing.T) {
	a := New(config.ChatBackend{Type: config.ChatStdio}, nil)
	require.Equal(t, os.Stdin, a.in)
	require.Equal(t, os.Stdout, a.out)
	require.Equal(t, replyDrain, a.drain)
}

func Test_Run_replies_in_order(t *testing.T) {
	a, out := newTest("hello\nworld\n")
	var keys []string
	submit := func(msg conversation.Message) error {
		keys = append(keys, msg.Conversation)
		echo(context.Background(), msg)
		return nil
	}
	require.NoError(t, a.Run(context.Background(), submit))
	require.Equal(t, []string{"stdin", "stdin"}, keys)
	require.Contains(t, out.String(), "pong:hello")
	require.Contains(t, out.String(), "pong:world")
	require.Contains(t, out.String(), "> ")
}

func Test_Run_skips_blank_and_quits(t *testing.T) {
	for _, stop := range []string{"/quit", "/exit"} {
		a, out := newTest("\n  \n" + stop + "\nafter\n")
		var count int
		require.NoError(t, a.Run(context.Background(), func(conversation.Message) error { count++; return nil }))
		require.Zero(t, count) // blank lines skipped, stop before "after"
		require.NotContains(t, out.String(), "after")
	}
}

func Test_Run_busy_continues(t *testing.T) {
	a, out := newTest("one\ntwo\n")
	var count int
	submit := func(msg conversation.Message) error {
		count++
		if count == 1 {
			return conversation.ErrBusy
		}
		echo(context.Background(), msg)
		return nil
	}
	require.NoError(t, a.Run(context.Background(), submit))
	require.Equal(t, 2, count)
	require.Contains(t, out.String(), "busy; try again shortly")
	require.Contains(t, out.String(), "pong:two")
}

func Test_Run_closed_stops(t *testing.T) {
	a, _ := newTest("one\ntwo\n")
	var count int
	submit := func(conversation.Message) error { count++; return conversation.ErrClosed }
	require.NoError(t, a.Run(context.Background(), submit))
	require.Equal(t, 1, count) // stops after the manager refuses as closed
}

func Test_Run_context_cancelled_waiting(t *testing.T) {
	a, out := newTest("one\ntwo\n")
	ctx, cancel := context.WithCancel(context.Background())
	// Accept the message but never reply, cancelling instead; deliver returns once the drain window elapses.
	submit := func(conversation.Message) error { cancel(); return nil }
	require.NoError(t, a.Run(ctx, submit))
	require.NotContains(t, out.String(), "one") // no reply ever arrived
}

func Test_Run_reply_during_drain(t *testing.T) {
	a, out := newTest("one\n")
	ctx, cancel := context.WithCancel(context.Background())
	// Shutdown starts before the reply is ready; the accepted message still gets its reply within the drain.
	submit := func(msg conversation.Message) error {
		cancel()
		go func() {
			time.Sleep(10 * time.Millisecond)
			_ = msg.Reply(context.Background(), conversation.Reply{Text: "late:" + msg.Text})
		}()
		return nil
	}
	require.NoError(t, a.Run(ctx, submit))
	require.Contains(t, out.String(), "late:one")
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("pipe broke") }

func Test_Run_read_error(t *testing.T) {
	a := &Adapter{in: errReader{}, out: &bytes.Buffer{}, logger: slog.Default()}
	err := a.Run(context.Background(), func(conversation.Message) error { return nil })
	require.ErrorContains(t, err, "read input: pipe broke")
}
