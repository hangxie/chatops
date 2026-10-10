package slack

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/config"
	"github.com/hangxie/chatops/internal/conversation"
)

// fakeSocket delivers test events and records acknowledgements.
type fakeSocket struct {
	events chan socketmode.Event
	runErr chan error

	ackErr error

	mu    sync.Mutex
	acked []string
}

func newFakeSocket() *fakeSocket {
	return &fakeSocket{events: make(chan socketmode.Event), runErr: make(chan error, 1)}
}

func (s *fakeSocket) Events() <-chan socketmode.Event { return s.events }

func (s *fakeSocket) RunContext(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-s.runErr:
		return err
	}
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

// fakeWeb serves auth.test and records chat.postMessage forms.
type fakeWeb struct {
	server *httptest.Server
	posts  chan url.Values
	authOK bool
	// hold, when set, keeps chat.postMessage from answering until it is closed.
	hold chan struct{}
}

func newFakeWeb(t *testing.T) *fakeWeb {
	t.Helper()
	w := &fakeWeb{posts: make(chan url.Values, 16), authOK: true}
	w.server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		rw.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/auth.test":
			if !w.authOK {
				_, _ = rw.Write([]byte(`{"ok":false,"error":"invalid_auth"}`))
				return
			}
			_, _ = rw.Write([]byte(`{"ok":true,"user_id":"UBOT"}`))
		case "/chat.postMessage":
			w.posts <- r.PostForm
			if w.hold != nil {
				select {
				case <-w.hold:
				case <-r.Context().Done():
				}
			}
			_, _ = rw.Write([]byte(`{"ok":true,"channel":"C1","ts":"1.1"}`))
		default:
			http.NotFound(rw, r)
		}
	}))
	t.Cleanup(w.server.Close)
	return w
}

func (w *fakeWeb) client() *slackapi.Client {
	return slackapi.New("xoxb-test", slackapi.OptionAPIURL(w.server.URL+"/"))
}

func (w *fakeWeb) nextPost(t *testing.T) url.Values {
	t.Helper()
	select {
	case form := <-w.posts:
		return form
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for chat.postMessage")
		return nil
	}
}

type harness struct {
	socket    *fakeSocket
	web       *fakeWeb
	submitted chan conversation.Message
	// submitErrs supplies submit's results in order; nil once drained.
	submitErrs chan error
	cancel     context.CancelFunc
	// result waits for Run once; later calls return the same error.
	result func() error
}

func run(t *testing.T, mutate func(*harness)) *harness {
	t.Helper()
	h := &harness{socket: newFakeSocket(), web: newFakeWeb(t), submitted: make(chan conversation.Message, 16), submitErrs: make(chan error, 16)}
	if mutate != nil {
		mutate(h)
	}
	adapter := newAdapter(h.socket, h.web.client(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	submit := func(msg conversation.Message) error {
		h.submitted <- msg
		select {
		case err := <-h.submitErrs:
			return err
		default:
			return nil
		}
	}
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, submit) }()
	h.result = sync.OnceValue(func() error { return <-done })
	t.Cleanup(func() {
		cancel()
		_ = h.result()
	})
	return h
}

func (h *harness) send(t *testing.T, event socketmode.Event) {
	t.Helper()
	select {
	case h.socket.events <- event:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out: the adapter is not reading events")
	}
}

func (h *harness) nextSubmitted(t *testing.T) conversation.Message {
	t.Helper()
	select {
	case msg := <-h.submitted:
		return msg
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a submitted message")
		return conversation.Message{}
	}
}

func eventsAPI(envelope string, event slackevents.EventsAPIEvent) socketmode.Event {
	return socketmode.Event{Type: socketmode.EventTypeEventsAPI, Data: event, Request: &socketmode.Request{EnvelopeID: envelope}}
}

func Test_Adapter_Run_submits_and_replies_in_thread(t *testing.T) {
	h := run(t, nil)
	h.send(t, eventsAPI("e1", mention(func(e *slackevents.AppMentionEvent) { e.ThreadTimeStamp = "1699999999.000001" })))

	msg := h.nextSubmitted(t)
	require.Equal(t, "slack:T1:C1:1699999999.000001", msg.Conversation)
	require.Equal(t, "U1", msg.User)
	require.Equal(t, "ping the server", msg.Text)
	require.Equal(t, []string{"e1"}, h.socket.ackedIDs())

	require.NoError(t, msg.Reply(context.Background(), "pong <!channel> & done"))
	form := h.web.nextPost(t)
	require.Equal(t, "C1", form.Get("channel"))
	require.Equal(t, "1699999999.000001", form.Get("thread_ts"))
	require.Equal(t, "pong &lt;!channel&gt; &amp; done", form.Get("text"), "replies are escaped so model output cannot ping or link")
	require.Equal(t, "false", form.Get("unfurl_links"))
	require.Equal(t, "false", form.Get("unfurl_media"))
}

func Test_Adapter_Run_ignores_retries(t *testing.T) {
	h := run(t, nil)
	h.send(t, eventsAPI("e1", mention(nil)))
	h.nextSubmitted(t)
	h.send(t, eventsAPI("e2", mention(nil)))
	h.send(t, eventsAPI("e3", direct(nil)))
	require.Equal(t, "D1", strings.Split(h.nextSubmitted(t).Conversation, ":")[2])
	require.Empty(t, h.submitted, "the retried mention is not submitted twice")
	require.Equal(t, []string{"e1", "e2", "e3"}, h.socket.ackedIDs())
}

func Test_Adapter_Run_acks_and_skips_other_events(t *testing.T) {
	h := run(t, nil)
	h.send(t, socketmode.Event{Type: socketmode.EventTypeInteractive, Data: slackapi.InteractionCallback{}, Request: &socketmode.Request{EnvelopeID: "i1"}})
	h.send(t, socketmode.Event{Type: socketmode.EventTypeConnected})
	h.send(t, eventsAPI("e1", mention(func(e *slackevents.AppMentionEvent) { e.Text = "no mention" })))
	h.send(t, socketmode.Event{Type: socketmode.EventTypeEventsAPI, Data: "unexpected", Request: &socketmode.Request{EnvelopeID: "e2"}})
	h.send(t, eventsAPI("e3", mention(nil)))
	h.nextSubmitted(t)
	require.Empty(t, h.submitted)
	require.Equal(t, []string{"i1", "e1", "e2", "e3"}, h.socket.ackedIDs())
}

func Test_Adapter_Run_keeps_going_after_failures(t *testing.T) {
	h := run(t, func(h *harness) {
		h.socket.ackErr = errors.New("write failed")
		h.submitErrs <- conversation.ErrClosed
		h.submitErrs <- conversation.ErrClosed
	})
	h.send(t, socketmode.Event{Type: socketmode.EventTypeConnectionError})
	h.send(t, socketmode.Event{Type: socketmode.EventTypeInvalidAuth})
	h.send(t, eventsAPI("e1", mention(nil)))
	h.nextSubmitted(t)
	h.send(t, eventsAPI("e2", direct(nil)))
	h.nextSubmitted(t)
	require.Empty(t, h.web.posts, "a closed manager gets no busy reply")
}

func Test_socket(t *testing.T) {
	client := socketmode.New(slackapi.New("xoxb-test", slackapi.OptionAppLevelToken("xapp-test")))
	s := socket{client: client}
	require.Equal(t, (<-chan socketmode.Event)(client.Events), s.Events())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// With ctx already cancelled both calls return at once; the SDK may report either as nil.
	_ = s.Ack(ctx, &socketmode.Request{EnvelopeID: "e1"})
	_ = s.RunContext(ctx)
}

func Test_Adapter_Run_busy_reply(t *testing.T) {
	h := run(t, func(h *harness) { h.submitErrs <- conversation.ErrBusy })
	h.send(t, eventsAPI("e1", mention(nil)))
	h.nextSubmitted(t)
	form := h.web.nextPost(t)
	require.Equal(t, "1700000000.000100", form.Get("thread_ts"))
	require.Equal(t, busyReply, form.Get("text"))
}

func Test_Adapter_Run_busy_reply_does_not_block_events(t *testing.T) {
	h := run(t, func(h *harness) {
		h.web.hold = make(chan struct{})
		for range maxBusyReplies + 1 {
			h.submitErrs <- conversation.ErrBusy
		}
	})
	defer close(h.web.hold)

	for i := range maxBusyReplies + 1 {
		ts := fmt.Sprintf("1700000001.%06d", i)
		h.send(t, eventsAPI(ts, mention(func(e *slackevents.AppMentionEvent) { e.TimeStamp = ts })))
		h.nextSubmitted(t)
	}
	for range maxBusyReplies {
		require.Equal(t, busyReply, h.web.nextPost(t).Get("text"))
	}

	h.send(t, eventsAPI("e-next", direct(nil)))
	require.Equal(t, "ping the server", h.nextSubmitted(t).Text, "events keep flowing while busy replies are stuck")
	require.Empty(t, h.web.posts, "busy replies beyond the bound are skipped, not queued")
}

func Test_Adapter_Run_dedupes_only_admitted_messages(t *testing.T) {
	h := run(t, func(h *harness) { h.submitErrs <- conversation.ErrBusy })
	h.send(t, eventsAPI("e1", mention(nil)))
	h.nextSubmitted(t)
	h.web.nextPost(t)

	h.send(t, eventsAPI("e2", mention(nil)))
	h.nextSubmitted(t)
	h.send(t, eventsAPI("e3", mention(nil)))
	h.send(t, eventsAPI("e4", direct(nil)))
	require.Equal(t, "slack:T1:D1:1700000000.000200", h.nextSubmitted(t).Conversation)
	require.Empty(t, h.submitted, "a redelivered message is submitted again only until it is admitted")
}

func Test_Adapter_Run_exit(t *testing.T) {
	t.Run("cancelled", func(t *testing.T) {
		h := run(t, nil)
		h.send(t, socketmode.Event{Type: socketmode.EventTypeConnected})
		h.cancel()
		require.NoError(t, h.result())
	})

	t.Run("socket-failure", func(t *testing.T) {
		h := run(t, nil)
		h.socket.runErr <- errors.New("invalid_auth")
		require.EqualError(t, h.result(), "slack: socket mode: invalid_auth")
	})

	t.Run("socket-stopped", func(t *testing.T) {
		h := run(t, nil)
		h.socket.runErr <- nil
		require.EqualError(t, h.result(), "slack: socket mode stopped unexpectedly")
	})

	t.Run("events-closed", func(t *testing.T) {
		h := run(t, nil)
		close(h.socket.events)
		require.EqualError(t, h.result(), "slack: socket mode event stream closed")
	})

	t.Run("auth-failure", func(t *testing.T) {
		h := run(t, func(h *harness) { h.web.authOK = false })
		require.ErrorContains(t, h.result(), "slack: identify bot: invalid_auth")
	})
}

func Test_limitReply(t *testing.T) {
	require.Equal(t, "short", limitReply("short"))
	long := strings.Repeat("é", maxReplyRunes+1)
	got := limitReply(long)
	require.True(t, strings.HasSuffix(got, "\n[reply truncated]"))
	require.Equal(t, maxReplyRunes, len([]rune(strings.TrimSuffix(got, "\n[reply truncated]"))))
}

func Test_New(t *testing.T) {
	t.Setenv("CHATOPS_TEST_SLACK_BOT", "xoxb-1")
	t.Setenv("CHATOPS_TEST_SLACK_APP", "xapp-1")

	_, err := New(config.Slack{BotTokenEnv: "CHATOPS_TEST_SLACK_BOT", AppTokenEnv: "CHATOPS_TEST_SLACK_APP"}, nil)
	require.NoError(t, err)
	_, err = New(config.Slack{BotTokenEnv: "CHATOPS_TEST_SLACK_MISSING", AppTokenEnv: "CHATOPS_TEST_SLACK_APP"}, nil)
	require.EqualError(t, err, "slack bot token: environment variable CHATOPS_TEST_SLACK_MISSING is not set")
	_, err = New(config.Slack{BotTokenEnv: "CHATOPS_TEST_SLACK_BOT", AppTokenEnv: "CHATOPS_TEST_SLACK_MISSING"}, nil)
	require.EqualError(t, err, "slack app token: environment variable CHATOPS_TEST_SLACK_MISSING is not set")
}
