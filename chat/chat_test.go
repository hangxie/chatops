package chat_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/chat"
)

// fakeConn is a minimal in-memory chat.Conn used to exercise the
// interface contract the way a real backend is expected to behave. It
// serves one conversation whose ID is "fake-conv".
type fakeConn struct {
	inbox  []chat.Message
	sent   []chat.Message
	closed bool
}

func (f *fakeConn) Receive(ctx context.Context) (chat.Message, error) {
	if err := ctx.Err(); err != nil {
		return chat.Message{}, err
	}
	if f.closed {
		return chat.Message{}, fmt.Errorf("receive: %w", chat.ErrClosed)
	}
	if len(f.inbox) == 0 {
		return chat.Message{}, fmt.Errorf("connection lost: %w", chat.ErrClosed)
	}
	msg := f.inbox[0]
	f.inbox = f.inbox[1:]
	return msg, nil
}

func (f *fakeConn) Send(_ context.Context, msg chat.Message) error {
	if f.closed {
		return fmt.Errorf("send: %w", chat.ErrClosed)
	}
	if msg.ConversationID != "fake-conv" {
		return fmt.Errorf("conversation %q: %w", msg.ConversationID, chat.ErrUnknownConversation)
	}
	f.sent = append(f.sent, msg)
	return nil
}

func (f *fakeConn) Close() error {
	f.closed = true
	return nil
}

func Test_Conn_contract(t *testing.T) {
	now := time.Now()
	var conn chat.Conn = &fakeConn{
		inbox: []chat.Message{
			{ConversationID: "fake-conv", Sender: "alice", Text: "deploy api", Timestamp: now},
			{ConversationID: "fake-conv", Sender: "bob", Text: "status", Timestamp: now},
		},
	}

	// Messages arrive in order, carrying the backend-computed
	// conversation ID.
	first, err := conn.Receive(context.Background())
	require.NoError(t, err)
	require.Equal(t, "fake-conv", first.ConversationID)
	require.Equal(t, "alice", first.Sender)
	require.Equal(t, "deploy api", first.Text)

	second, err := conn.Receive(context.Background())
	require.NoError(t, err)
	require.Equal(t, "status", second.Text)

	// Replying with the received conversation ID succeeds; an ID the
	// backend does not know is reported via ErrUnknownConversation.
	err = conn.Send(context.Background(), chat.Message{ConversationID: first.ConversationID, Text: "on it"})
	require.NoError(t, err)
	err = conn.Send(context.Background(), chat.Message{ConversationID: "no-such-conv", Text: "on it"})
	require.ErrorIs(t, err, chat.ErrUnknownConversation)

	// A cancelled context surfaces its error.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = conn.Receive(cancelled)
	require.ErrorIs(t, err, context.Canceled)

	// After Close, Receive and Send report ErrClosed.
	require.NoError(t, conn.Close())
	_, err = conn.Receive(context.Background())
	require.ErrorIs(t, err, chat.ErrClosed)
	err = conn.Send(context.Background(), chat.Message{ConversationID: "fake-conv", Text: "late"})
	require.ErrorIs(t, err, chat.ErrClosed)
}

func Test_sentinel_errors_are_stable(t *testing.T) {
	testCases := map[string]struct {
		err     error
		message string
	}{
		"closed":               {err: chat.ErrClosed, message: "connection closed"},
		"unknown-conversation": {err: chat.ErrUnknownConversation, message: "unknown conversation"},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			require.EqualError(t, tc.err, tc.message)
			require.True(t, errors.Is(fmt.Errorf("wrapped: %w", tc.err), tc.err))
		})
	}
}
