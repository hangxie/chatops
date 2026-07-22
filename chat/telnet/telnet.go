// Package telnet implements a chat.Conn over a plain TCP text
// connection, the naive chat backend useful for local development and
// testing against tools like telnet or nc.
//
// The package exports Scheme and Opener for wiring the backend into
// a chat.Registry under the "telnet" URL scheme; the rest of the URL
// is the server address, with the port defaulting to the telnet
// port 23:
//
//	telnet://chat.example.com:6023
//	telnet://chat.example.com
//
// The wire protocol is bare lines of text: every newline-terminated
// line received is one inbound message (blank lines are ignored), and
// Send writes the message text followed by a newline, so text
// containing newlines reaches the peer as multiple lines. Telnet
// option negotiation (IAC sequences) is not performed. The connection
// carries a single conversation, identified by ConversationID; the
// protocol has no notion of identity, so Message.Sender is left
// empty.
package telnet

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hangxie/chatops/chat"
	"github.com/hangxie/chatops/cred"
)

// ConversationID is the conversation ID of the single conversation a
// telnet connection carries. Every received message carries it, and
// messages sent must carry it.
const ConversationID = "telnet"

// Scheme is the URL scheme this backend serves in a chat.Registry.
const Scheme = "telnet"

// Opener is the chat.OpenerFunc for this backend: the URL host is the
// server address, with the port defaulting to the telnet port 23.
func Opener(ctx context.Context, u *url.URL, _ cred.Store) (chat.Conn, error) {
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("telnet: URL %q has no host", u.String())
	}
	port := u.Port()
	if port == "" {
		port = "23"
	}
	return Open(ctx, net.JoinHostPort(host, port))
}

// Conn is a chat.Conn over a plain TCP text connection.
type Conn struct {
	conn net.Conn

	// msgs delivers inbound messages from readLoop to Receive; it is
	// closed when readLoop exits, with readErr holding the cause.
	msgs    chan chat.Message
	readErr error

	// done is closed by Close to stop readLoop even when it is
	// blocked delivering into msgs.
	done      chan struct{}
	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error

	writeMu sync.Mutex
}

// Open connects to the telnet chat server at addr ("host:port").
func Open(ctx context.Context, addr string) (*Conn, error) {
	var dialer net.Dialer
	netConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("telnet: %w", err)
	}
	c := &Conn{
		conn: netConn,
		msgs: make(chan chat.Message),
		done: make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

// readLoop owns the read side of the connection: it turns each
// non-blank line into a message on c.msgs and exits — closing c.msgs
// — when the connection fails or Close is called.
func (c *Conn) readLoop() {
	defer close(c.msgs)
	reader := bufio.NewReader(c.conn)
	for {
		line, err := reader.ReadString('\n')
		if text := strings.TrimRight(line, "\r\n"); text != "" {
			msg := chat.Message{
				ConversationID: ConversationID,
				Text:           text,
				Timestamp:      time.Now(),
			}
			select {
			case c.msgs <- msg:
			case <-c.done:
				return
			}
		}
		if err != nil {
			// Receive only reads readErr after c.msgs is closed, so
			// the channel close orders this write.
			c.readErr = err
			return
		}
	}
}

// Receive returns the next line received from the server as a
// message.
func (c *Conn) Receive(ctx context.Context) (chat.Message, error) {
	select {
	case <-ctx.Done():
		return chat.Message{}, fmt.Errorf("telnet: %w", ctx.Err())
	case msg, ok := <-c.msgs:
		if !ok {
			if c.closed.Load() {
				return chat.Message{}, fmt.Errorf("telnet: %w", chat.ErrClosed)
			}
			return chat.Message{}, fmt.Errorf("telnet: connection lost: %w", c.readErr)
		}
		return msg, nil
	}
}

// Send writes msg.Text to the server as a newline-terminated line.
// msg.ConversationID must be ConversationID, the connection's single
// conversation.
func (c *Conn) Send(ctx context.Context, msg chat.Message) error {
	if msg.ConversationID != ConversationID {
		return fmt.Errorf("telnet: conversation %q: %w", msg.ConversationID, chat.ErrUnknownConversation)
	}
	if c.closed.Load() {
		return fmt.Errorf("telnet: %w", chat.ErrClosed)
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	// Check ctx only after acquiring the lock: a Send that waited
	// behind another writer while its context was cancelled must not
	// transmit.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("telnet: %w", err)
	}

	// Fail the write when ctx is cancelled mid-flight by expiring the
	// write deadline from the cancellation callback.
	callbackDone := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(callbackDone)
		_ = c.conn.SetWriteDeadline(time.Now())
	})
	_, err := fmt.Fprintf(c.conn, "%s\n", msg.Text)
	// stop does not wait for an already-started callback, so before
	// clearing the deadline (and releasing writeMu) wait for the
	// callback ourselves — otherwise the clear could race it and
	// leave an expired deadline poisoning a later Send. When stop
	// wins, no deadline was set and none will be.
	if !stop() {
		<-callbackDone
		_ = c.conn.SetWriteDeadline(time.Time{})
	}

	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("telnet: %w", ctxErr)
		}
		if c.closed.Load() {
			return fmt.Errorf("telnet: %w", chat.ErrClosed)
		}
		return fmt.Errorf("telnet: send: %w", err)
	}
	return nil
}

// Close terminates the connection, unblocking any pending Receive.
// Only the first call has an effect; later calls return the first
// call's result.
func (c *Conn) Close() error {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		close(c.done)
		c.closeErr = c.conn.Close()
	})
	return c.closeErr
}
