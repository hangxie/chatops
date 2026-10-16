package chat

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/conversation"
)

// submitter captures the conversation keys submit sees, safely across backends.
type submitter struct {
	mu   sync.Mutex
	keys []string
}

func (s *submitter) submit(msg conversation.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, msg.Conversation)
	return nil
}

// oneShot submits a single message keyed "c" then returns err.
type oneShot struct{ err error }

func (b oneShot) Run(ctx context.Context, submit func(conversation.Message) error) error {
	_ = submit(conversation.Message{Conversation: "c"})
	return b.err
}

func (oneShot) Connected() bool { return false }

// blocker submits nothing and blocks until the context ends, then returns its error.
type blocker struct{ err error }

func (b blocker) Run(ctx context.Context, _ func(conversation.Message) error) error {
	<-ctx.Done()
	return b.err
}

func (blocker) Connected() bool { return true }

func Test_Run_prefixes_keys(t *testing.T) {
	s := &submitter{}
	backends := []Named{
		{Name: "eng-slack", Backend: oneShot{}},
		{Name: "console", Backend: oneShot{}},
	}
	require.NoError(t, Run(context.Background(), backends, s.submit))
	sort.Strings(s.keys)
	require.Equal(t, []string{"console:c", "eng-slack:c"}, s.keys)
}

func Test_Run_backend_error_stops_group(t *testing.T) {
	boom := errors.New("socket mode: invalid_auth")
	backends := []Named{
		{Name: "slack", Backend: oneShot{err: boom}},
		{Name: "console", Backend: blocker{}}, // cancelled when slack errors
	}
	require.ErrorIs(t, Run(context.Background(), backends, func(conversation.Message) error { return nil }), boom)
}

func Test_prefixed(t *testing.T) {
	var got conversation.Message
	wrapped := prefixed("console", func(msg conversation.Message) error { got = msg; return nil })
	require.NoError(t, wrapped(conversation.Message{Conversation: "stdin", User: "local"}))
	require.Equal(t, "console:stdin", got.Conversation)
	require.Equal(t, "console", got.Backend)
	require.Equal(t, "local", got.User)
}
