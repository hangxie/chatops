package reply_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/chat"
	"github.com/hangxie/chatops/tool"
	"github.com/hangxie/chatops/tool/reply"
)

// fakeConn is a minimal chat.Conn that records sent messages and can
// be told to fail sends with a fixed error.
type fakeConn struct {
	sent    []chat.Message
	sendErr error
	closed  bool
}

func (f *fakeConn) Receive(_ context.Context) (chat.Message, error) {
	return chat.Message{}, fmt.Errorf("fake: %w", chat.ErrClosed)
}

func (f *fakeConn) Send(_ context.Context, msg chat.Message) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, msg)
	return nil
}

func (f *fakeConn) Close() error {
	f.closed = true
	return nil
}

func Test_Open_nil_conn(t *testing.T) {
	tl, err := reply.Open(context.Background(), nil)
	require.Nil(t, tl)
	require.ErrorContains(t, err, "nil connection")
}

func Test_Invoke(t *testing.T) {
	testCases := map[string]struct {
		call    tool.Call
		sendErr error
		sent    []chat.Message
		errIs   error
		errMsg  string
	}{
		"send": {
			call: tool.Call{
				Action:     "send",
				Target:     "conv-1",
				Parameters: map[string]string{"text": "on it"},
			},
			sent: []chat.Message{{ConversationID: "conv-1", Text: "on it"}},
		},
		"unknown-action": {
			call:  tool.Call{Action: "shout", Target: "conv-1", Parameters: map[string]string{"text": "hi"}},
			errIs: tool.ErrUnknownAction,
		},
		"empty-action": {
			call:  tool.Call{Target: "conv-1", Parameters: map[string]string{"text": "hi"}},
			errIs: tool.ErrUnknownAction,
		},
		"missing-target": {
			call:   tool.Call{Action: "send", Parameters: map[string]string{"text": "hi"}},
			errMsg: "no target conversation",
		},
		"missing-text": {
			call:   tool.Call{Action: "send", Target: "conv-1", Parameters: map[string]string{}},
			errMsg: `no "text" parameter`,
		},
		"nil-parameters": {
			call:   tool.Call{Action: "send", Target: "conv-1"},
			errMsg: `no "text" parameter`,
		},
		"empty-text": {
			call:   tool.Call{Action: "send", Target: "conv-1", Parameters: map[string]string{"text": ""}},
			errMsg: `no "text" parameter`,
		},
		"send-failure": {
			call: tool.Call{
				Action:     "send",
				Target:     "no-such-conv",
				Parameters: map[string]string{"text": "hi"},
			},
			sendErr: fmt.Errorf("fake: %w", chat.ErrUnknownConversation),
			errIs:   chat.ErrUnknownConversation,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			conn := &fakeConn{sendErr: tc.sendErr}
			tl, err := reply.Open(context.Background(), conn)
			require.NoError(t, err)
			defer func() {
				require.NoError(t, tl.Close())
			}()

			result, err := tl.Invoke(context.Background(), tc.call)
			if tc.errIs != nil {
				require.ErrorIs(t, err, tc.errIs)
				return
			}
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			// Sending is the outcome; Text stays empty so callers that
			// post non-empty Result.Text to chat do not double-post.
			require.Equal(t, tool.Result{}, result)
			require.Equal(t, tc.sent, conn.sent)
		})
	}
}

func Test_Invoke_cancelled_context(t *testing.T) {
	conn := &fakeConn{}
	tl, err := reply.Open(context.Background(), conn)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = tl.Invoke(ctx, tool.Call{
		Action:     "send",
		Target:     "conv-1",
		Parameters: map[string]string{"text": "hi"},
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, conn.sent)
}

func Test_Close_does_not_close_conn(t *testing.T) {
	conn := &fakeConn{}
	tl, err := reply.Open(context.Background(), conn)
	require.NoError(t, err)

	require.NoError(t, tl.Close())
	require.NoError(t, tl.Close())
	require.False(t, conn.closed)
}

func Test_Tool_implements_tool_Tool(t *testing.T) {
	var _ tool.Tool = &reply.Tool{}
	require.Equal(t, "reply", reply.Scheme)
	require.Equal(t, "reply://", reply.URL)
}
