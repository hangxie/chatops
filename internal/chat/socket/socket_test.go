package socket

import (
	"bufio"
	"context"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/conversation"
)

// recorder submits with a canned reply, capturing the conversation keys it is called with.
type recorder struct {
	mu   sync.Mutex
	keys []string
	// hold accepts the message but never replies, to exercise shutdown while a turn is in flight.
	hold bool
	// captured, when set, receives each accepted message so a test can reply to it later.
	captured chan conversation.Message
	reply    func(text string) (string, error) // maps input -> reply, or an error to refuse
}

func (r *recorder) submit(msg conversation.Message) error {
	r.mu.Lock()
	r.keys = append(r.keys, msg.Conversation)
	r.mu.Unlock()
	if r.captured != nil {
		r.captured <- msg
	}
	if r.hold {
		return nil
	}
	out, err := r.reply(msg.Text)
	if err != nil {
		return err
	}
	return msg.Reply(context.Background(), out)
}

func echo(text string) (string, error) { return "pong:" + text, nil }

func start(t *testing.T, submit func(conversation.Message) error) (string, context.CancelFunc) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	server := New(ln, slog.Default())
	server.drain = 100 * time.Millisecond // keep the shutdown-drain path fast in tests
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, submit) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("server did not shut down")
		}
	})
	return ln.Addr().String(), cancel
}

func dial(t *testing.T, addr string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
	return conn, bufio.NewReader(conn)
}

func Test_Run_echo(t *testing.T) {
	rec := &recorder{reply: echo}
	addr, _ := start(t, rec.submit)
	conn, r := dial(t, addr)

	_, err := conn.Write([]byte("hello\n\nworld\n")) // the blank line is skipped
	require.NoError(t, err)
	line, err := r.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "pong:hello\n", line)
	line, err = r.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "pong:world\n", line)
}

func Test_Run_busy(t *testing.T) {
	rec := &recorder{reply: func(string) (string, error) { return "", conversation.ErrBusy }}
	addr, _ := start(t, rec.submit)
	conn, r := dial(t, addr)

	_, err := conn.Write([]byte("hi\n"))
	require.NoError(t, err)
	line, err := r.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "busy; try again shortly\n", line)
}

func Test_Run_closed_stops(t *testing.T) {
	rec := &recorder{reply: func(string) (string, error) { return "", conversation.ErrClosed }}
	addr, _ := start(t, rec.submit)
	conn, r := dial(t, addr)

	_, err := conn.Write([]byte("hi\n"))
	require.NoError(t, err)
	_, err = r.ReadString('\n') // the server closes the connection without replying
	require.Error(t, err)
}

func Test_Run_per_connection_keys(t *testing.T) {
	rec := &recorder{reply: echo}
	addr, _ := start(t, rec.submit)

	for _, want := range []string{"pong:a", "pong:b"} {
		conn, r := dial(t, addr)
		_, err := conn.Write([]byte(want[len("pong:"):] + "\n"))
		require.NoError(t, err)
		line, err := r.ReadString('\n')
		require.NoError(t, err)
		require.Equal(t, want+"\n", line)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.ElementsMatch(t, []string{"conn-1", "conn-2"}, rec.keys)
}

func Test_New_defaults_logger(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	require.NotNil(t, New(ln, nil).logger)
}

func Test_Run_drain_on_shutdown(t *testing.T) {
	rec := &recorder{hold: true} // accept the message, never reply
	addr, cancel := start(t, rec.submit)
	conn, r := dial(t, addr)
	_, err := conn.Write([]byte("hi\n"))
	require.NoError(t, err)

	time.Sleep(50 * time.Millisecond) // let the server reach the reply wait
	cancel()                          // shutdown: the drain window elapses with no reply, then the conn closes
	_, err = r.ReadString('\n')
	require.Error(t, err)
}

func Test_Run_drain_delivers_reply(t *testing.T) {
	rec := &recorder{hold: true, captured: make(chan conversation.Message, 1)}
	addr, cancel := start(t, rec.submit)
	conn, r := dial(t, addr)
	_, err := conn.Write([]byte("slow\n"))
	require.NoError(t, err)

	msg := <-rec.captured // the server accepted the message and is waiting for its reply
	cancel()              // shutdown begins; the connection stays open through the drain
	require.NoError(t, msg.Reply(context.Background(), "finished during drain"))
	line, err := r.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "finished during drain\n", line)
}

func Test_write_deadline(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	s := &Server{logger: slog.Default(), writeTimeout: 20 * time.Millisecond}
	require.False(t, s.write(server, "nobody is reading")) // the peer never reads, so the write times out
}

func Test_Run_accept_error(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	require.NoError(t, ln.Close()) // Accept fails immediately, and ctx is still live
	err = New(ln, slog.Default()).Run(context.Background(), func(conversation.Message) error { return nil })
	require.ErrorContains(t, err, "accept")
}

func Test_Run_shutdown_closes_connections(t *testing.T) {
	rec := &recorder{reply: echo}
	addr, cancel := start(t, rec.submit)
	conn, r := dial(t, addr)
	_, err := conn.Write([]byte("hi\n"))
	require.NoError(t, err)
	_, err = r.ReadString('\n')
	require.NoError(t, err)

	cancel() // shutdown closes the live connection, so the next read ends
	_, err = r.ReadString('\n')
	require.Error(t, err)
}
