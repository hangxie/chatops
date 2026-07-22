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
	"github.com/hangxie/chatops/cred"
	"github.com/hangxie/chatops/internal/testutils"
)

func slackCredentials() testutils.CredentialStore {
	return testutils.CredentialStore{Values: map[cred.Key]string{
		cred.SlackBotToken: "xoxb-test",
		cred.SlackAppToken: "xapp-test",
	}}
}

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
	choices []chat.Choice
}

type fakeAPI struct {
	mu          sync.Mutex
	posted      []postedMessage
	err         error
	botUserID   string
	identityErr error
	timestamp   string
	cleared     []postedMessage
}

func newFakeAPI() *fakeAPI { return &fakeAPI{botUserID: "UCHATOPS", timestamp: "20.1"} }

func (a *fakeAPI) BotUserID(context.Context) (string, error) {
	return a.botUserID, a.identityErr
}

func (a *fakeAPI) PostMessage(_ context.Context, channel, thread string, msg chat.Message) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.posted = append(a.posted, postedMessage{channel: channel, thread: thread, text: msg.Text, choices: msg.Choices})
	return a.timestamp, a.err
}

func (a *fakeAPI) ClearChoices(_ context.Context, channel, timestamp, text string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cleared = append(a.cleared, postedMessage{channel: channel, thread: timestamp, text: text})
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
			_, err = Opener(context.Background(), u, slackCredentials())
			require.ErrorContains(t, err, "takes no configuration")
		})
	}
}

func Test_Open_requires_credentials(t *testing.T) {
	testErr := errors.New("store failed")
	testCases := map[string]struct {
		credentials cred.Store
		errMsg      string
		errIs       error
	}{
		"no-store":  {errMsg: "credential store is not configured"},
		"bot-token": {credentials: testutils.CredentialStore{Values: map[cred.Key]string{cred.SlackAppToken: "xapp-test"}}, errMsg: cred.SlackBotToken.String(), errIs: cred.ErrNotFound},
		"app-token": {credentials: testutils.CredentialStore{Values: map[cred.Key]string{cred.SlackBotToken: "xoxb-test"}}, errMsg: cred.SlackAppToken.String(), errIs: cred.ErrNotFound},
		"empty-bot-token": {credentials: testutils.CredentialStore{Values: map[cred.Key]string{
			cred.SlackBotToken: "", cred.SlackAppToken: "xapp-test",
		}}, errMsg: cred.SlackBotToken.String()},
		"empty-app-token": {credentials: testutils.CredentialStore{Values: map[cred.Key]string{
			cred.SlackBotToken: "xoxb-test", cred.SlackAppToken: "",
		}}, errMsg: cred.SlackAppToken.String()},
		"store-error": {credentials: testutils.CredentialStore{Err: testErr}, errMsg: "resolve", errIs: testErr},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, err := Open(context.Background(), tc.credentials)
			require.ErrorContains(t, err, tc.errMsg)
			if tc.errIs != nil {
				require.ErrorIs(t, err, tc.errIs)
			}
		})
	}
}

func Test_Open_via_registry(t *testing.T) {
	registry := chat.NewRegistry(chat.Backend{Scheme: Scheme, Opener: Opener})
	_, err := registry.Open(context.Background(), "slack://", nil)
	require.ErrorContains(t, err, "credential store is not configured")
}

func Test_open_startup(t *testing.T) {
	credentials := slackCredentials()
	t.Run("connected", func(t *testing.T) {
		socket := newFakeSocket()
		conn, err := open(context.Background(), credentials, func(botToken, appToken string) (socketClient, messageAPI) {
			require.Equal(t, "xoxb-test", botToken)
			require.Equal(t, "xapp-test", appToken)
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
		_, err := open(context.Background(), credentials, func(_, _ string) (socketClient, messageAPI) {
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
		_, err := open(ctx, credentials, func(_, _ string) (socketClient, messageAPI) {
			return socket, newFakeAPI()
		})
		require.ErrorIs(t, err, context.Canceled)
	})
}

func Test_open_cancelled_context(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	factoryCalled := false
	_, err := open(ctx, slackCredentials(), func(_, _ string) (socketClient, messageAPI) {
		factoryCalled = true
		return newFakeSocket(), newFakeAPI()
	})
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, factoryCalled)
}

func Test_open_identifies_bot(t *testing.T) {
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
			_, err := open(context.Background(), slackCredentials(), func(_, _ string) (socketClient, messageAPI) {
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

func choiceEvent(envelopeID, actionID, value string) socketmode.Event {
	return socketmode.Event{
		Type: socketmode.EventTypeInteractive,
		Data: slackapi.InteractionCallback{
			Type:      slackapi.InteractionTypeBlockActions,
			User:      slackapi.User{ID: "U2"},
			Message:   slackapi.Message{Msg: slackapi.Msg{Text: "continue?"}},
			Container: slackapi.Container{ChannelID: "C1", MessageTs: "20.1"},
			ActionCallback: slackapi.ActionCallbacks{BlockActions: []*slackapi.BlockAction{{
				ActionID: actionID, Value: value, ActionTs: "30.1",
			}}},
		},
		Request: &socketmode.Request{EnvelopeID: envelopeID},
	}
}

func Test_Send_and_receive_choices(t *testing.T) {
	conn, socket, api := testConn(t)
	socket.events <- messageEvent("E1", &slackevents.MessageEvent{
		Channel: "C1", User: "U1", Text: "<@UCHATOPS> ping it", TimeStamp: "10.1",
	})
	inbound, err := conn.Receive(context.Background())
	require.NoError(t, err)
	choices := []chat.Choice{{Label: "Yes", Value: "yes"}, {Label: "No", Value: "no"}}
	require.NoError(t, conn.Send(context.Background(), chat.Message{
		ConversationID: inbound.ConversationID, Text: "continue?", Choices: choices,
	}))
	require.Equal(t, choices, api.messages()[0].choices)

	socket.events <- choiceEvent("E2", choiceActionID(0), "yes")
	answer, err := conn.Receive(context.Background())
	require.NoError(t, err)
	require.Equal(t, inbound.ConversationID, answer.ConversationID)
	require.Equal(t, "U2", answer.Sender)
	require.Equal(t, "yes", answer.Text)
	require.Equal(t, []postedMessage{{channel: "C1", thread: "20.1", text: "continue?"}}, api.cleared)
	require.Eventually(t, func() bool { return len(socket.ackedIDs()) == 2 }, time.Second, time.Millisecond)
}

func Test_Receive_rejects_invalid_and_duplicate_choices(t *testing.T) {
	conn, socket, _ := testConn(t)
	socket.events <- messageEvent("E1", &slackevents.MessageEvent{
		Channel: "C1", User: "U1", Text: "<@UCHATOPS> ping it", TimeStamp: "10.1",
	})
	inbound, err := conn.Receive(context.Background())
	require.NoError(t, err)
	require.NoError(t, conn.Send(context.Background(), chat.Message{
		ConversationID: inbound.ConversationID,
		Text:           "continue?",
		Choices:        []chat.Choice{{Label: "Yes", Value: "yes"}},
	}))

	socket.events <- choiceEvent("bad", "foreign", "yes")
	socket.events <- choiceEvent("unregistered", choiceActionID(1), "no")
	socket.events <- choiceEvent("valid", choiceActionID(0), "yes")
	answer, err := conn.Receive(context.Background())
	require.NoError(t, err)
	require.Equal(t, "yes", answer.Text)

	socket.events <- choiceEvent("duplicate", choiceActionID(0), "yes")
	socket.events <- messageEvent("next", &slackevents.MessageEvent{
		Channel: "C1", User: "U1", Text: "<@UCHATOPS> next", TimeStamp: "40.1",
	})
	next, err := conn.Receive(context.Background())
	require.NoError(t, err)
	require.Equal(t, "next", next.Text)
	require.Eventually(t, func() bool { return len(socket.ackedIDs()) == 6 }, time.Second, time.Millisecond)
}

func Test_Send_rejects_invalid_choice(t *testing.T) {
	conn, socket, api := testConn(t)
	socket.events <- messageEvent("E1", &slackevents.MessageEvent{
		Channel: "C1", User: "U1", Text: "<@UCHATOPS> hello", TimeStamp: "10.1",
	})
	msg, err := conn.Receive(context.Background())
	require.NoError(t, err)
	err = conn.Send(context.Background(), chat.Message{
		ConversationID: msg.ConversationID, Text: "continue?",
		Choices: []chat.Choice{{Label: "Deploy"}},
	})
	require.ErrorContains(t, err, "invalid choice")
	require.Empty(t, api.messages())
}

func Test_Send_choices_requires_posted_timestamp(t *testing.T) {
	conn, socket, api := testConn(t)
	api.timestamp = ""
	socket.events <- messageEvent("E1", &slackevents.MessageEvent{
		Channel: "C1", User: "U1", Text: "<@UCHATOPS> hello", TimeStamp: "10.1",
	})
	msg, err := conn.Receive(context.Background())
	require.NoError(t, err)
	err = conn.Send(context.Background(), chat.Message{
		ConversationID: msg.ConversationID, Text: "continue?",
		Choices: []chat.Choice{{Label: "Yes", Value: "yes"}},
	})
	require.ErrorContains(t, err, "empty message timestamp")
}

func Test_Receive_reports_clear_choices_failure(t *testing.T) {
	conn, socket, api := testConn(t)
	socket.events <- messageEvent("E1", &slackevents.MessageEvent{
		Channel: "C1", User: "U1", Text: "<@UCHATOPS> hello", TimeStamp: "10.1",
	})
	msg, err := conn.Receive(context.Background())
	require.NoError(t, err)
	require.NoError(t, conn.Send(context.Background(), chat.Message{
		ConversationID: msg.ConversationID, Text: "continue?",
		Choices: []chat.Choice{{Label: "Yes", Value: "yes"}},
	}))
	testErr := errors.New("update failed")
	api.err = testErr
	socket.events <- choiceEvent("E2", choiceActionID(0), "yes")
	_, err = conn.Receive(context.Background())
	require.ErrorIs(t, err, testErr)
	require.ErrorContains(t, err, "clear choices")
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
	timestamp, err := (&webAPI{client: client}).PostMessage(context.Background(), "C1", "10.1", chat.Message{Text: "on it"})
	require.NoError(t, err)
	require.Equal(t, "10.2", timestamp)
	require.Equal(t, postedMessage{channel: "C1", thread: "10.1", text: "on it"}, <-requests)
}

func Test_slackAPI_PostMessage_choices(t *testing.T) {
	blocks := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		blocks <- r.Form.Get("blocks")
		_, err := w.Write([]byte(`{"ok":true,"channel":"C1","ts":"20.1"}`))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	client := slackapi.New("xoxb-test", slackapi.OptionAPIURL(server.URL+"/"))
	_, err := (&webAPI{client: client}).PostMessage(context.Background(), "C1", "10.1", chat.Message{
		Text:    "continue?",
		Choices: []chat.Choice{{Label: "Yes", Value: "yes"}, {Label: "No", Value: "no"}},
	})
	require.NoError(t, err)
	payload := <-blocks
	require.Contains(t, payload, `"type":"actions"`)
	require.Contains(t, payload, `"action_id":"chatops.choice.0"`)
	require.Contains(t, payload, `"action_id":"chatops.choice.1"`)
	require.Contains(t, payload, `"value":"no"`)
}

func Test_slackAPI_PostMessage_rejects_invalid_choice(t *testing.T) {
	_, err := (&webAPI{client: slackapi.New("xoxb-test")}).PostMessage(
		context.Background(), "C1", "10.1",
		chat.Message{Text: "continue?", Choices: []chat.Choice{{Label: "Yes"}}},
	)
	require.ErrorContains(t, err, "invalid choice")
}

func Test_slackAPI_ClearChoices(t *testing.T) {
	request := make(chan postedMessage, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		request <- postedMessage{channel: r.Form.Get("channel"), thread: r.Form.Get("ts"), text: r.Form.Get("blocks")}
		_, err := w.Write([]byte(`{"ok":true,"channel":"C1","ts":"20.1"}`))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	client := slackapi.New("xoxb-test", slackapi.OptionAPIURL(server.URL+"/"))
	require.NoError(t, (&webAPI{client: client}).ClearChoices(context.Background(), "C1", "20.1", "yes"))
	require.Equal(t, postedMessage{channel: "C1", thread: "20.1", text: "[]"}, <-request)
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
