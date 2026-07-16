// Package chat provides a generic interface for connecting a bot to
// chat backends such as Slack, Discord, Mattermost, or a plain telnet
// chat.
//
// Each backend lives in its own sub-package and exports its URL
// scheme and opener; callers wire the backends they support into a
// Registry, so a connection can be opened from a single URL:
//
//	reg := chat.NewRegistry(
//		chat.Backend{Scheme: telnet.Scheme, Opener: telnet.Opener},
//	)
//	conn, err := reg.Open(ctx, "telnet://chat.example.com:6023")
//
// Messages are grouped into conversations — a topic or thread that a
// piece of work is about. Each backend computes a stable conversation
// ID from its native addressing (for example a Slack backend derives
// it from channel and thread, while a telnet backend has a single
// conversation) and translates it back on send. Callers treat the ID
// as an opaque string scoped to one Conn: to reply to a message, send
// with the ConversationID of the message being answered.
//
// Credentials for connecting to a backend are never part of the URL;
// backends take them from their standard environment variables (e.g.
// SLACK_BOT_TOKEN).
package chat

import (
	"context"
	"errors"
	"time"
)

// ErrClosed is the sentinel error reported by Receive and Send after
// the connection has been closed with Close. Backends wrap it with
// context, so check for it with errors.Is.
var ErrClosed = errors.New("connection closed")

// ErrUnknownConversation is the sentinel error reported by Send when
// the message's ConversationID does not map to a conversation the
// backend knows. Backends wrap it with context, so check for it with
// errors.Is.
var ErrUnknownConversation = errors.New("unknown conversation")

// Message is a single chat message received from or sent to a
// backend.
type Message struct {
	// ConversationID identifies the topic or thread the message
	// belongs to. It is computed by the backend from its native
	// addressing, is opaque to callers, and is scoped to the Conn
	// that produced it. On send it selects the conversation to post
	// into.
	ConversationID string

	// Sender identifies who sent the message, in backend-native form
	// (e.g. a Slack user ID). It may be empty for backends without a
	// notion of identity. It is informational on send; backends post
	// as the connected bot identity.
	Sender string

	// Text is the message body.
	Text string

	// Timestamp is when the message was sent as reported by the
	// backend, or the local receive time for backends that do not
	// report one. It is ignored on send.
	Timestamp time.Time
}

// Conn is an open connection to a chat backend.
//
// Implementations must be safe for concurrent use by multiple
// goroutines, including calling Close to unblock a pending Receive.
type Conn interface {
	// Receive returns the next inbound message. It blocks until a
	// message arrives, ctx is done, the connection is lost, or Close
	// is called. After Close it reports an error wrapping ErrClosed.
	Receive(ctx context.Context) (Message, error)

	// Send posts msg.Text into the conversation identified by
	// msg.ConversationID. It returns an error wrapping
	// ErrUnknownConversation when the ID does not map to a
	// conversation the backend knows, and an error wrapping
	// ErrClosed after Close.
	Send(ctx context.Context, msg Message) error

	// Close terminates the connection and releases its resources,
	// unblocking any pending Receive. Only the first call has an
	// effect.
	Close() error
}
