package reply_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hangxie/chatops/chat"
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
	// A nil connection is a wiring mistake, caught at the wiring site rather
	// than surfacing as a runtime error nobody can act on.
	require.PanicsWithValue(t, "reply: open with nil connection", func() {
		reply.Open(context.Background(), nil)
	})
}

func Test_Definition(t *testing.T) {
	def := reply.Definition()
	require.Equal(t, reply.ToolName, def.Name)
	require.NotEmpty(t, def.Description)

	// The schema is inferred from Args, so it declares the text the model
	// supplies — and, deliberately, no conversation: that is host state the
	// model never chooses.
	schema, ok := def.InputSchema.(*jsonschema.Schema)
	require.True(t, ok)
	require.Contains(t, schema.Properties, "text")
	require.Contains(t, schema.Properties, "choices")
	require.NotContains(t, schema.Properties, "conversation")
	require.Equal(t, []string{"text"}, schema.Required)
}

func Test_mustSchema_panics_on_failure(t *testing.T) {
	require.PanicsWithValue(t, "reply: infer input schema: broken", func() {
		reply.MustSchemaForTest(nil, errors.New("broken"))
	})
}

func Test_mustResolve_panics_on_failure(t *testing.T) {
	require.PanicsWithValue(t, "reply: resolve input schema: broken", func() {
		reply.MustResolveForTest(nil, errors.New("broken"))
	})
}

func Test_Handler(t *testing.T) {
	conversation := "conv-1"

	testCases := map[string]struct {
		args         map[string]any
		conversation string
		noConv       bool
		sendErr      error
		sent         []chat.Message
		errIs        error
		errMsg       string
	}{
		"send": {
			args:         map[string]any{"text": "on it"},
			conversation: conversation,
			sent:         []chat.Message{{ConversationID: conversation, Text: "on it"}},
		},
		"send-with-choices": {
			args: map[string]any{"text": "continue?", "choices": []any{
				map[string]any{"label": "Yes", "value": "yes"},
				map[string]any{"label": "No", "value": "no"},
			}},
			conversation: conversation,
			sent: []chat.Message{{
				ConversationID: conversation,
				Text:           "continue?",
				Choices: []chat.Choice{
					{Label: "Yes", Value: "yes"},
					{Label: "No", Value: "no"},
				},
			}},
		},
		"missing-conversation": {
			args:   map[string]any{"text": "hi"},
			noConv: true,
			errMsg: "no target conversation",
		},
		"empty-conversation": {
			args:         map[string]any{"text": "hi"},
			conversation: "",
			errMsg:       "no target conversation",
		},
		"missing-text": {
			args:         map[string]any{},
			conversation: conversation,
			errMsg:       `no "text" argument`,
		},
		"nil-arguments": {
			args:         nil,
			conversation: conversation,
			errMsg:       `no "text" argument`,
		},
		"empty-text": {
			args:         map[string]any{"text": ""},
			conversation: conversation,
			errMsg:       `no "text" argument`,
		},
		"bad-argument-type": {
			args:         map[string]any{"text": 42},
			conversation: conversation,
			errMsg:       "invalid arguments",
		},
		// The schema is the contract even though nothing between the host and
		// this tool enforces it: a server-side tool would have had these
		// rejected before the handler ran.
		"choice missing value": {
			args:         map[string]any{"text": "pick", "choices": []any{map[string]any{"label": "Yes"}}},
			conversation: conversation,
			errMsg:       "invalid arguments",
		},
		"choice missing label": {
			args:         map[string]any{"text": "pick", "choices": []any{map[string]any{"value": "yes"}}},
			conversation: conversation,
			errMsg:       "invalid arguments",
		},
		"choice of the wrong type": {
			args:         map[string]any{"text": "pick", "choices": []any{"yes"}},
			conversation: conversation,
			errMsg:       "invalid arguments",
		},
		"undeclared field": {
			args:         map[string]any{"text": "hi", "conversation": "somewhere-else"},
			conversation: conversation,
			errMsg:       "invalid arguments",
		},
		"unencodable-argument": {
			args:         map[string]any{"text": make(chan int)},
			conversation: conversation,
			errMsg:       "encode arguments",
		},
		"send-failure": {
			args:         map[string]any{"text": "hi"},
			conversation: "no-such-conv",
			sendErr:      fmt.Errorf("fake: %w", chat.ErrUnknownConversation),
			errIs:        chat.ErrUnknownConversation,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			conn := &fakeConn{sendErr: tc.sendErr}
			tl := reply.Open(context.Background(), conn)
			defer func() {
				require.NoError(t, tl.Close())
			}()

			ctx := context.Background()
			if !tc.noConv {
				ctx = chat.WithConversation(ctx, tc.conversation)
			}
			result, err := tl.Handler(ctx, tc.args)
			if tc.errIs != nil {
				require.ErrorIs(t, err, tc.errIs)
				return
			}
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			// Posting is the outcome, so the result carries no content and a
			// caller relaying tool output does not double-post.
			require.Empty(t, result.Content)
			require.False(t, result.IsError)
			require.Equal(t, tc.sent, conn.sent)
		})
	}
}

func Test_Invoke_cancelled_context(t *testing.T) {
	conn := &fakeConn{}
	tl := reply.Open(context.Background(), conn)

	ctx, cancel := context.WithCancel(chat.WithConversation(context.Background(), "conv-1"))
	cancel()
	_, err := tl.Invoke(ctx, reply.Args{Text: "hi"})
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, conn.sent)
}

func Test_Close_does_not_close_conn(t *testing.T) {
	conn := &fakeConn{}
	tl := reply.Open(context.Background(), conn)

	require.NoError(t, tl.Close())
	require.NoError(t, tl.Close())
	require.False(t, conn.closed)
}

// acceptsHostHandler takes exactly the calling convention mcphost.HostTool
// requires, so passing Handler to it fails to compile if the signature drifts
// and the reply tool could no longer be registered with the host.
func acceptsHostHandler(func(context.Context, map[string]any) (*mcp.CallToolResult, error)) {}

func Test_Handler_matches_host_tool_signature(t *testing.T) {
	conn := &fakeConn{}
	tl := reply.Open(context.Background(), conn)

	acceptsHostHandler(tl.Handler)
	require.Equal(t, "reply", reply.ToolName)
}
