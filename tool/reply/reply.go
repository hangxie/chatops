// Package reply implements a tool.Tool that posts text back into a
// chat conversation, so planners can express "say this to the
// requester" as an ordinary tool step alongside operational tool
// calls.
//
// Unlike other tools, a reply tool is bound to a live chat.Conn — the
// connection the message being answered arrived on — rather than to
// an endpoint of its own, so it exports no tool.OpenerFunc and cannot
// be opened from a URL through a tool.Registry. Callers open it
// directly with Open(ctx, conn) and make it available to plan
// execution under the conventional bare URL:
//
//	reply://
//
// The only supported action is "send": Call.Target is the
// conversation ID to post into (chat.Message.ConversationID of the
// message being answered) and Call.Parameters["text"] is the text to
// post. Sending is the whole outcome, so Result.Text stays empty and
// callers that post non-empty Result.Text back to chat do not
// double-post.
package reply

import (
	"context"
	"errors"
	"fmt"

	"github.com/hangxie/chatops/chat"
	"github.com/hangxie/chatops/tool"
)

// Scheme names the conventional URL ("reply://") under which callers
// make an opened reply tool available to plan execution. There is no
// tool.OpenerFunc for it; see the package documentation.
const Scheme = "reply"

// Tool posts text into conversations of one chat connection.
type Tool struct {
	conn chat.Conn
}

// Open returns a reply tool bound to conn. The connection stays owned
// by the caller: the tool sends on it but never closes it.
func Open(_ context.Context, conn chat.Conn) (*Tool, error) {
	if conn == nil {
		return nil, errors.New("reply: open with nil connection")
	}
	return &Tool{conn: conn}, nil
}

// Invoke answers the "send" action by posting
// call.Parameters["text"] into the conversation identified by
// call.Target, and reports an error wrapping tool.ErrUnknownAction
// for any other action. A missing target or missing/empty text is an
// error; errors from the underlying send (e.g. wrapping
// chat.ErrUnknownConversation) are passed through wrapped.
func (t *Tool) Invoke(ctx context.Context, call tool.Call) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, fmt.Errorf("reply: %w", err)
	}
	if call.Action != "send" {
		return tool.Result{}, fmt.Errorf("reply: %q: %w", call.Action, tool.ErrUnknownAction)
	}
	if call.Target == "" {
		return tool.Result{}, errors.New("reply: send with no target conversation")
	}
	text := call.Parameters["text"]
	if text == "" {
		return tool.Result{}, errors.New(`reply: send with no "text" parameter`)
	}
	msg := chat.Message{ConversationID: call.Target, Text: text}
	if err := t.conn.Send(ctx, msg); err != nil {
		return tool.Result{}, fmt.Errorf("reply: %w", err)
	}
	return tool.Result{}, nil
}

// Close releases nothing; the chat connection is owned by the caller
// and stays open.
func (t *Tool) Close() error {
	return nil
}
