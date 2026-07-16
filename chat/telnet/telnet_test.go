package telnet_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/chat"
	"github.com/hangxie/chatops/chat/telnet"
)

// server is a single-connection TCP test server standing in for a
// naive telnet chat server.
type server struct {
	listener net.Listener
	conns    chan net.Conn
}

func startServer(t *testing.T) *server {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	s := &server{listener: listener, conns: make(chan net.Conn, 1)}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			s.conns <- conn
		}
	}()
	return s
}

func (s *server) addr() string {
	return s.listener.Addr().String()
}

// accept returns the server side of the next accepted connection.
func (s *server) accept(t *testing.T) net.Conn {
	t.Helper()
	select {
	case conn := <-s.conns:
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for client connection")
		return nil
	}
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// testRegistry wires the backend into a chat.Registry the way a
// caller is expected to.
func testRegistry() *chat.Registry {
	return chat.NewRegistry(chat.Backend{Scheme: telnet.Scheme, Opener: telnet.Opener})
}

func Test_Open_via_registry(t *testing.T) {
	s := startServer(t)
	conn, err := testRegistry().Open(testCtx(t), "telnet://"+s.addr())
	require.NoError(t, err)
	defer func() {
		require.NoError(t, conn.Close())
	}()
	s.accept(t)
}

func Test_Open_registry_url_errors(t *testing.T) {
	testCases := map[string]struct {
		url    string
		errMsg string
	}{
		"no-host":        {url: "telnet://", errMsg: "no host"},
		"no-host-opaque": {url: "telnet:", errMsg: "no host"},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, err := testRegistry().Open(testCtx(t), tc.url)
			require.ErrorContains(t, err, tc.errMsg)
		})
	}
}

func Test_Open_registry_default_port(t *testing.T) {
	// Without an explicit port the backend dials the telnet default,
	// port 23. Nothing is expected to listen there, so the dial error
	// names it; in the unlikely case something does listen, connecting
	// at all still proves the default was applied.
	conn, err := testRegistry().Open(testCtx(t), "telnet://127.0.0.1")
	if err == nil {
		require.NoError(t, conn.Close())
		return
	}
	require.ErrorContains(t, err, ":23")
}

func Test_Open_dial_failure(t *testing.T) {
	// Grab a free port, then close the listener so the dial is refused.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := listener.Addr().String()
	require.NoError(t, listener.Close())

	_, err = telnet.Open(testCtx(t), addr)
	require.ErrorContains(t, err, "telnet:")
}

func Test_Open_cancelled_context(t *testing.T) {
	s := startServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := telnet.Open(ctx, s.addr())
	require.ErrorIs(t, err, context.Canceled)
}

func Test_Receive(t *testing.T) {
	s := startServer(t)
	conn, err := telnet.Open(testCtx(t), s.addr())
	require.NoError(t, err)
	defer func() {
		require.NoError(t, conn.Close())
	}()
	remote := s.accept(t)

	// LF and CRLF line endings are both accepted and stripped; blank
	// lines are not messages.
	before := time.Now()
	_, err = remote.Write([]byte("deploy api\r\n\r\nstatus\n"))
	require.NoError(t, err)

	first, err := conn.Receive(testCtx(t))
	require.NoError(t, err)
	require.Equal(t, telnet.ConversationID, first.ConversationID)
	require.Equal(t, "deploy api", first.Text)
	require.Empty(t, first.Sender)
	require.False(t, first.Timestamp.Before(before))

	second, err := conn.Receive(testCtx(t))
	require.NoError(t, err)
	require.Equal(t, telnet.ConversationID, second.ConversationID)
	require.Equal(t, "status", second.Text)
}

func Test_Receive_context_cancelled(t *testing.T) {
	s := startServer(t)
	conn, err := telnet.Open(testCtx(t), s.addr())
	require.NoError(t, err)
	defer func() {
		require.NoError(t, conn.Close())
	}()
	s.accept(t)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err = conn.Receive(ctx)
	require.ErrorIs(t, err, context.Canceled)
}

func Test_Receive_connection_lost(t *testing.T) {
	s := startServer(t)
	conn, err := telnet.Open(testCtx(t), s.addr())
	require.NoError(t, err)
	defer func() {
		require.NoError(t, conn.Close())
	}()
	remote := s.accept(t)

	// A partial line before the peer disconnects is still delivered.
	_, err = remote.Write([]byte("last words"))
	require.NoError(t, err)
	require.NoError(t, remote.Close())

	msg, err := conn.Receive(testCtx(t))
	require.NoError(t, err)
	require.Equal(t, "last words", msg.Text)

	_, err = conn.Receive(testCtx(t))
	require.ErrorContains(t, err, "connection lost")
	require.ErrorIs(t, err, io.EOF)
}

func Test_Send(t *testing.T) {
	s := startServer(t)
	conn, err := telnet.Open(testCtx(t), s.addr())
	require.NoError(t, err)
	defer func() {
		require.NoError(t, conn.Close())
	}()
	remote := s.accept(t)

	err = conn.Send(testCtx(t), chat.Message{ConversationID: telnet.ConversationID, Text: "on it"})
	require.NoError(t, err)

	require.NoError(t, remote.SetReadDeadline(time.Now().Add(5*time.Second)))
	line, err := bufio.NewReader(remote).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "on it\n", line)
}

func Test_Send_unknown_conversation(t *testing.T) {
	s := startServer(t)
	conn, err := telnet.Open(testCtx(t), s.addr())
	require.NoError(t, err)
	defer func() {
		require.NoError(t, conn.Close())
	}()
	s.accept(t)

	testCases := map[string]string{
		"empty":         "",
		"foreign-id":    "slack:C123:170000.42",
		"close-but-not": telnet.ConversationID + "-2",
	}

	for name, conversationID := range testCases {
		t.Run(name, func(t *testing.T) {
			err := conn.Send(testCtx(t), chat.Message{ConversationID: conversationID, Text: "on it"})
			require.ErrorIs(t, err, chat.ErrUnknownConversation)
		})
	}
}

func Test_Send_cancelled_context(t *testing.T) {
	s := startServer(t)
	conn, err := telnet.Open(testCtx(t), s.addr())
	require.NoError(t, err)
	defer func() {
		require.NoError(t, conn.Close())
	}()
	s.accept(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = conn.Send(ctx, chat.Message{ConversationID: telnet.ConversationID, Text: "on it"})
	require.ErrorIs(t, err, context.Canceled)
}

func waitSendErr(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for Send to return")
		return nil
	}
}

func Test_Send_cancelled_while_waiting_for_writer(t *testing.T) {
	s := startServer(t)
	conn, err := telnet.Open(testCtx(t), s.addr())
	require.NoError(t, err)
	remote := s.accept(t)

	// Send A: a payload large enough to block in the socket write
	// until the server drains it, keeping the write lock held.
	big := strings.Repeat("a", 8<<20)
	aDone := make(chan error, 1)
	go func() {
		aDone <- conn.Send(testCtx(t), chat.Message{ConversationID: telnet.ConversationID, Text: big})
	}()
	time.Sleep(100 * time.Millisecond) // let A take the lock and block

	// Send B: queues behind A, and its context is cancelled while it
	// waits. It must fail without ever transmitting.
	ctxB, cancelB := context.WithCancel(context.Background())
	bDone := make(chan error, 1)
	go func() {
		bDone <- conn.Send(ctxB, chat.Message{ConversationID: telnet.ConversationID, Text: "from-b"})
	}()
	time.Sleep(100 * time.Millisecond) // let B block on the lock
	cancelB()

	// Drain the server side so A can finish.
	drained := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(remote)
		drained <- string(data)
	}()

	require.NoError(t, waitSendErr(t, aDone))
	require.ErrorIs(t, waitSendErr(t, bDone), context.Canceled)

	// Close so the drain goroutine sees EOF, then verify only A's
	// payload ever reached the wire.
	require.NoError(t, conn.Close())
	select {
	case data := <-drained:
		require.Equal(t, big+"\n", data)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out draining server side")
	}
}

func Test_Send_cancelled_mid_write(t *testing.T) {
	s := startServer(t)
	conn, err := telnet.Open(testCtx(t), s.addr())
	require.NoError(t, err)
	defer func() {
		require.NoError(t, conn.Close())
	}()
	s.accept(t) // never drained, so a large write blocks

	ctx, cancel := context.WithCancel(context.Background())
	big := strings.Repeat("a", 8<<20)
	done := make(chan error, 1)
	go func() {
		done <- conn.Send(ctx, chat.Message{ConversationID: telnet.ConversationID, Text: big})
	}()
	time.Sleep(100 * time.Millisecond) // let the write fill the socket buffer and block
	cancel()

	require.ErrorIs(t, waitSendErr(t, done), context.Canceled)
}

func Test_Send_closed_mid_write(t *testing.T) {
	s := startServer(t)
	conn, err := telnet.Open(testCtx(t), s.addr())
	require.NoError(t, err)
	s.accept(t) // never drained, so a large write blocks

	big := strings.Repeat("a", 8<<20)
	done := make(chan error, 1)
	go func() {
		done <- conn.Send(testCtx(t), chat.Message{ConversationID: telnet.ConversationID, Text: big})
	}()
	time.Sleep(100 * time.Millisecond) // let the write fill the socket buffer and block
	require.NoError(t, conn.Close())

	require.ErrorIs(t, waitSendErr(t, done), chat.ErrClosed)
}

func Test_Send_write_error(t *testing.T) {
	s := startServer(t)
	conn, err := telnet.Open(testCtx(t), s.addr())
	require.NoError(t, err)
	defer func() {
		require.NoError(t, conn.Close())
	}()
	remote := s.accept(t)

	// Peer hangs up; writes keep landing in local buffers until the
	// reset surfaces, so send until the failure shows.
	require.NoError(t, remote.Close())
	var sendErr error
	for range 100 {
		sendErr = conn.Send(testCtx(t), chat.Message{ConversationID: telnet.ConversationID, Text: "hello?"})
		if sendErr != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// A plain network failure: not a cancellation, not a local close.
	require.ErrorContains(t, sendErr, "send:")
}

func Test_Close_with_undelivered_message(t *testing.T) {
	s := startServer(t)
	conn, err := telnet.Open(testCtx(t), s.addr())
	require.NoError(t, err)
	remote := s.accept(t)

	// A message nobody ever Receives: the reader goroutine blocks
	// delivering it, and Close must still shut everything down.
	_, err = remote.Write([]byte("never received\n"))
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond) // let the reader block on delivery
	require.NoError(t, conn.Close())

	// The pending message is dropped in favor of shutdown.
	_, err = conn.Receive(testCtx(t))
	require.ErrorIs(t, err, chat.ErrClosed)
}

func Test_Send_cancel_race_does_not_poison_later_sends(t *testing.T) {
	s := startServer(t)
	conn, err := telnet.Open(testCtx(t), s.addr())
	require.NoError(t, err)
	defer func() {
		require.NoError(t, conn.Close())
	}()
	remote := s.accept(t)

	// Drain continuously so sends complete quickly.
	go func() { _, _ = io.Copy(io.Discard, remote) }()

	// Race cancellation against write completion over and over. The
	// raced Send may or may not fail, but a follow-up Send with a
	// healthy context must never inherit an expired write deadline
	// from the cancellation callback.
	for range 200 {
		ctx, cancel := context.WithCancel(context.Background())
		cancelled := make(chan struct{})
		go func() {
			cancel()
			close(cancelled)
		}()
		_ = conn.Send(ctx, chat.Message{ConversationID: telnet.ConversationID, Text: "racing"})
		<-cancelled

		err := conn.Send(testCtx(t), chat.Message{ConversationID: telnet.ConversationID, Text: "follow-up"})
		require.NoError(t, err)
	}
}

func Test_Close(t *testing.T) {
	s := startServer(t)
	conn, err := telnet.Open(testCtx(t), s.addr())
	require.NoError(t, err)
	s.accept(t)

	// Close unblocks a pending Receive with ErrClosed.
	errCh := make(chan error, 1)
	go func() {
		_, err := conn.Receive(context.Background())
		errCh <- err
	}()
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, conn.Close())
	select {
	case err := <-errCh:
		require.ErrorIs(t, err, chat.ErrClosed)
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not unblock pending Receive")
	}

	// After Close, both Receive and Send report ErrClosed, and closing
	// again is harmless.
	_, err = conn.Receive(testCtx(t))
	require.ErrorIs(t, err, chat.ErrClosed)
	err = conn.Send(testCtx(t), chat.Message{ConversationID: telnet.ConversationID, Text: "late"})
	require.ErrorIs(t, err, chat.ErrClosed)
	require.NoError(t, conn.Close())
}

func Test_concurrent_send_receive(t *testing.T) {
	s := startServer(t)
	conn, err := telnet.Open(testCtx(t), s.addr())
	require.NoError(t, err)
	defer func() {
		require.NoError(t, conn.Close())
	}()
	remote := s.accept(t)

	const n = 20
	go func() {
		for i := range n {
			_, _ = fmt.Fprintf(remote, "inbound %d\n", i)
		}
	}()
	sendErrs := make(chan error, 1)
	go func() {
		for i := range n {
			msg := chat.Message{ConversationID: telnet.ConversationID, Text: fmt.Sprintf("outbound %d", i)}
			if err := conn.Send(testCtx(t), msg); err != nil {
				sendErrs <- err
				return
			}
		}
		sendErrs <- nil
	}()

	for i := range n {
		msg, err := conn.Receive(testCtx(t))
		require.NoError(t, err)
		require.Equal(t, fmt.Sprintf("inbound %d", i), msg.Text)
	}
	require.NoError(t, <-sendErrs)

	reader := bufio.NewReader(remote)
	require.NoError(t, remote.SetReadDeadline(time.Now().Add(5*time.Second)))
	for i := range n {
		line, err := reader.ReadString('\n')
		require.NoError(t, err)
		require.Equal(t, fmt.Sprintf("outbound %d\n", i), line)
	}
}
