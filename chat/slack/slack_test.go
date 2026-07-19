package slack

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/chat"
)

type fakeSocket struct {
	events chan socketmode.Event

	mu        sync.Mutex
	acked     []string
	ackErr    error
	runErr    error
	connected bool
	started   chan struct{}
	startOne  sync.Once
}

func newFakeSocket() *fakeSocket {
	return &fakeSocket{events: make(chan socketmode.Event, 20), connected: true, started: make(chan struct{})}
}

func (s *fakeSocket) Events() <-chan socketmode.Event { return s.events }

func (s *fakeSocket) RunContext(ctx context.Context) error {
	s.startOne.Do(func() { close(s.started) })
	if s.connected {
		s.events <- socketmode.Event{Type: socketmode.EventTypeConnected}
	}
	if s.runErr != nil {
		return s.runErr
	}
	<-ctx.Done()
	return ctx.Err()
}

func (s *fakeSocket) Ack(_ context.Context, req *socketmode.Request) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acked = append(s.acked, req.EnvelopeID)
	return s.ackErr
}

func (s *fakeSocket) ackedIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.acked...)
}

type postedMessage struct {
	channel string
	thread  string
	text    string
}

type fakeAPI struct {
	mu          sync.Mutex
	posted      []postedMessage
	err         error
	botUserID   string
	identityErr error
}

func newFakeAPI() *fakeAPI { return &fakeAPI{botUserID: "UCHATOPS"} }

func (a *fakeAPI) BotUserID(context.Context) (string, error) {
	return a.botUserID, a.identityErr
}

func (a *fakeAPI) PostMessage(_ context.Context, channel, thread, text string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.posted = append(a.posted, postedMessage{channel: channel, thread: thread, text: text})
	return a.err
}

func (a *fakeAPI) messages() []postedMessage {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]postedMessage(nil), a.posted...)
}

func testConn(t *testing.T) (*Conn, *fakeSocket, *fakeAPI) {
	t.Helper()
	socket := newFakeSocket()
	api := newFakeAPI()
	conn := newConn(socket, api, api.botUserID)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	select {
	case <-socket.started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Socket Mode to start")
	}
	return conn, socket, api
}

func Test_Opener_rejects_configuration(t *testing.T) {
	t.Setenv(BotTokenEnv, "xoxb-test")
	t.Setenv(AppTokenEnv, "xapp-test")
	testCases := map[string]string{
		"host":     "slack://workspace",
		"path":     "slack:///channel",
		"query":    "slack://?channel=C123",
		"fragment": "slack://#fragment",
	}
	for name, rawURL := range testCases {
		t.Run(name, func(t *testing.T) {
			u, err := url.Parse(rawURL)
			require.NoError(t, err)
			_, err = Opener(context.Background(), u)
			require.ErrorContains(t, err, "takes no configuration")
		})
	}
}

func Test_Open_requires_tokens(t *testing.T) {
	testCases := map[string]struct {
		botToken string
		appToken string
		errMsg   string
	}{
		"bot-token": {appToken: "xapp-test", errMsg: BotTokenEnv},
		"app-token": {botToken: "xoxb-test", errMsg: AppTokenEnv},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Setenv(BotTokenEnv, tc.botToken)
			t.Setenv(AppTokenEnv, tc.appToken)
			_, err := Open(context.Background())
			require.ErrorContains(t, err, tc.errMsg)
		})
	}
}

func Test_Open_via_registry(t *testing.T) {
	t.Setenv(BotTokenEnv, "")
	t.Setenv(AppTokenEnv, "")
	registry := chat.NewRegistry(chat.Backend{Scheme: Scheme, Opener: Opener})
	_, err := registry.Open(context.Background(), "slack://")
	require.ErrorContains(t, err, BotTokenEnv)
}

func Test_open_startup(t *testing.T) {
	tokens := map[string]string{BotTokenEnv: "xoxb-test", AppTokenEnv: "xapp-test"}
	getenv := func(name string) string { return tokens[name] }
	t.Run("connected", func(t *testing.T) {
		socket := newFakeSocket()
		conn, err := open(context.Background(), getenv, func(_, _ string) (socketClient, messageAPI) {
			return socket, newFakeAPI()
		})
		require.NoError(t, err)
		require.NoError(t, conn.Close())
	})
	t.Run("connection-error", func(t *testing.T) {
		testErr := errors.New("invalid auth")
		socket := newFakeSocket()
		socket.connected = false
		socket.runErr = testErr
		_, err := open(context.Background(), getenv, func(_, _ string) (socketClient, messageAPI) {
			return socket, newFakeAPI()
		})
		require.ErrorIs(t, err, testErr)
	})
	t.Run("context-cancelled-while-connecting", func(t *testing.T) {
		socket := newFakeSocket()
		socket.connected = false
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-socket.started
			cancel()
		}()
		_, err := open(ctx, getenv, func(_, _ string) (socketClient, messageAPI) {
			return socket, newFakeAPI()
		})
		require.ErrorIs(t, err, context.Canceled)
	})
}

func Test_open_cancelled_context(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	factoryCalled := false
	_, err := open(ctx, func(string) string { return "token" }, func(_, _ string) (socketClient, messageAPI) {
		factoryCalled = true
		return newFakeSocket(), newFakeAPI()
	})
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, factoryCalled)
}

func Test_open_identifies_bot(t *testing.T) {
	tokens := map[string]string{BotTokenEnv: "xoxb-test", AppTokenEnv: "xapp-test"}
	testErr := errors.New("auth failed")
	testCases := map[string]struct {
		api    *fakeAPI
		errMsg string
		errIs  error
	}{
		"auth-error": {api: &fakeAPI{identityErr: testErr}, errMsg: "identify bot", errIs: testErr},
		"empty-id":   {api: &fakeAPI{}, errMsg: "empty user ID"},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, err := open(context.Background(), func(key string) string { return tokens[key] }, func(_, _ string) (socketClient, messageAPI) {
				return newFakeSocket(), tc.api
			})
			require.ErrorContains(t, err, tc.errMsg)
			if tc.errIs != nil {
				require.ErrorIs(t, err, tc.errIs)
			}
		})
	}
}

func Test_Send_replies_to_mapped_thread(t *testing.T) {
	conn, socket, api := testConn(t)
	socket.events <- messageEvent("E1", &slackevents.MessageEvent{Channel: "C1", User: "U1", Text: "<@UCHATOPS> hello", TimeStamp: "10.1"})
	msg, err := conn.Receive(context.Background())
	require.NoError(t, err)
	require.NoError(t, conn.Send(context.Background(), chat.Message{ConversationID: msg.ConversationID, Text: "on it"}))
	require.Equal(t, []postedMessage{{channel: "C1", thread: "10.1", text: "on it"}}, api.messages())
}

func Test_Send_errors(t *testing.T) {
	conn, socket, api := testConn(t)
	err := conn.Send(context.Background(), chat.Message{ConversationID: "slack:C1:unknown", Text: "no"})
	require.ErrorIs(t, err, chat.ErrUnknownConversation)

	socket.events <- messageEvent("E1", &slackevents.MessageEvent{Channel: "C1", User: "U1", Text: "<@UCHATOPS> hello", TimeStamp: "10.1"})
	msg, err := conn.Receive(context.Background())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = conn.Send(ctx, chat.Message{ConversationID: msg.ConversationID, Text: "no"})
	require.ErrorIs(t, err, context.Canceled)

	testErr := errors.New("post failed")
	api.err = testErr
	err = conn.Send(context.Background(), chat.Message{ConversationID: msg.ConversationID, Text: "no"})
	require.ErrorIs(t, err, testErr)
}

func Test_Receive_context_and_connection_errors(t *testing.T) {
	t.Run("context", func(t *testing.T) {
		conn, _, _ := testConn(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := conn.Receive(ctx)
		require.ErrorIs(t, err, context.Canceled)
	})
	t.Run("socket", func(t *testing.T) {
		testErr := errors.New("socket failed")
		socket := newFakeSocket()
		socket.runErr = testErr
		api := newFakeAPI()
		conn := newConn(socket, api, api.botUserID)
		t.Cleanup(func() { require.NoError(t, conn.Close()) })
		_, err := conn.Receive(context.Background())
		require.ErrorIs(t, err, testErr)
		require.ErrorContains(t, err, "connection lost")
	})
	t.Run("ack", func(t *testing.T) {
		conn, socket, _ := testConn(t)
		testErr := errors.New("ack failed")
		socket.ackErr = testErr
		socket.events <- messageEvent("E1", &slackevents.MessageEvent{Channel: "C1", User: "U1", Text: "<@UCHATOPS> hello", TimeStamp: "10.1"})
		_, err := conn.Receive(context.Background())
		require.ErrorIs(t, err, testErr)
		require.ErrorContains(t, err, "acknowledge event")
	})
}

func Test_readLoop_closed_socket_result(t *testing.T) {
	conn := &Conn{
		socket:    newFakeSocket(),
		ctx:       context.Background(),
		msgs:      make(chan chat.Message),
		done:      make(chan struct{}),
		startup:   make(chan error, 1),
		runResult: make(chan error, 1),
	}
	conn.closed.Store(true)
	conn.runResult <- errors.New("socket stopped")
	go conn.readLoop()
	<-conn.done
	require.ErrorIs(t, conn.readErr, chat.ErrClosed)
}

func Test_readLoop_cancelled_while_delivering(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	socket := newFakeSocket()
	conn := &Conn{
		socket:    socket,
		botID:     "UCHATOPS",
		ctx:       ctx,
		msgs:      make(chan chat.Message),
		done:      make(chan struct{}),
		routes:    newRouteCache(defaultConversationTTL, defaultMaxConversationRoutes),
		startup:   make(chan error, 1),
		runResult: make(chan error),
	}
	go conn.readLoop()
	socket.events <- messageEvent("E1", &slackevents.MessageEvent{
		Channel: "C1", User: "U1", Text: "<@UCHATOPS> ping", TimeStamp: "10.1",
	})
	require.Eventually(t, func() bool { return len(socket.ackedIDs()) == 1 }, time.Second, time.Millisecond)
	cancel()
	<-conn.done
	require.ErrorIs(t, conn.readErr, chat.ErrClosed)
}

func Test_Close(t *testing.T) {
	conn, _, _ := testConn(t)
	receiveDone := make(chan error, 1)
	go func() {
		_, err := conn.Receive(context.Background())
		receiveDone <- err
	}()
	require.NoError(t, conn.Close())
	require.NoError(t, conn.Close())
	require.ErrorIs(t, <-receiveDone, chat.ErrClosed)
	require.ErrorIs(t, conn.Send(context.Background(), chat.Message{}), chat.ErrClosed)
}

func Test_slackAPI_PostMessage(t *testing.T) {
	requests := make(chan postedMessage, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		requests <- postedMessage{channel: r.Form.Get("channel"), thread: r.Form.Get("thread_ts"), text: r.Form.Get("text")}
		_, err := w.Write([]byte(`{"ok":true,"channel":"C1","ts":"10.2"}`))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	client := slackapi.New("xoxb-test", slackapi.OptionAPIURL(server.URL+"/"))
	require.NoError(t, (&webAPI{client: client}).PostMessage(context.Background(), "C1", "10.1", "on it"))
	require.Equal(t, postedMessage{channel: "C1", thread: "10.1", text: "on it"}, <-requests)
}

func Test_slackAPI_BotUserID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/auth.test", r.URL.Path)
		_, err := w.Write([]byte(`{"ok":true,"user_id":"UCHATOPS"}`))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	client := slackapi.New("xoxb-test", slackapi.OptionAPIURL(server.URL+"/"))
	userID, err := (&webAPI{client: client}).BotUserID(context.Background())
	require.NoError(t, err)
	require.Equal(t, "UCHATOPS", userID)
}

func Test_slackAPI_BotUserID_error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write([]byte(`{"ok":false,"error":"invalid_auth"}`))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	client := slackapi.New("invalid", slackapi.OptionAPIURL(server.URL+"/"))
	_, err := (&webAPI{client: client}).BotUserID(context.Background())
	require.ErrorContains(t, err, "invalid_auth")
}

func Test_socketAdapter(t *testing.T) {
	client := slackapi.New("xoxb-test", slackapi.OptionAppLevelToken("xapp-test"))
	socket := socketmode.New(client)
	adapter := &socketAdapter{client: socket}
	require.Equal(t, (<-chan socketmode.Event)(socket.Events), adapter.Events())
	require.NoError(t, adapter.Ack(context.Background(), &socketmode.Request{EnvelopeID: "E1"}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, adapter.RunContext(ctx), context.Canceled)
}

func Test_defaultClients(t *testing.T) {
	socket, api := defaultClients("xoxb-test", "xapp-test")
	require.IsType(t, &socketAdapter{}, socket)
	require.IsType(t, &webAPI{}, api)
}
