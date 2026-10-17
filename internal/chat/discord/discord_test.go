package discord

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/config"
	"github.com/hangxie/chatops/internal/conversation"
)

type sent struct {
	channel string
	msg     *discordgo.MessageSend
}

// fakeGateway delivers test events and records sent messages.
type fakeGateway struct {
	events    chan any
	openErr   chan error
	threads   map[string]bool
	lookupErr error
	closeErr  error
	closed    atomic.Bool
	opened    atomic.Bool
	// closedEarly records a Close that ran before Open returned.
	closedEarly atomic.Bool

	mu      sync.Mutex
	sendErr error
	sends   chan sent
}

func newFakeGateway() *fakeGateway {
	return &fakeGateway{events: make(chan any), openErr: make(chan error, 1), sends: make(chan sent, 16)}
}

func (g *fakeGateway) Open() error {
	defer g.opened.Store(true)
	return <-g.openErr
}

func (g *fakeGateway) Close() error {
	g.closedEarly.Store(!g.opened.Load())
	g.closed.Store(true)
	return g.closeErr
}

func (g *fakeGateway) IsThread(_ context.Context, id string) (bool, error) {
	return g.threads[id], g.lookupErr
}

func (g *fakeGateway) Events() <-chan any         { return g.events }
func (g *fakeGateway) setSendErr(err error)       { g.mu.Lock(); g.sendErr = err; g.mu.Unlock() }
func (g *fakeGateway) nextSend(t *testing.T) sent { t.Helper(); return next(t, g.sends) }

func (g *fakeGateway) Send(_ context.Context, channel string, msg *discordgo.MessageSend) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sends <- sent{channel: channel, msg: msg}
	return g.sendErr
}

func next[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting")
		var zero T
		return zero
	}
}

// running starts Run against gw and returns its error channel and the cancel that stops it.
func running(t *testing.T, gw *fakeGateway, submit func(conversation.Message) error) (*Adapter, <-chan error, context.CancelFunc) {
	t.Helper()
	a := newAdapter(gw, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, submit) }()
	t.Cleanup(cancel)
	return a, done, cancel
}

func mention(text string) *discordgo.MessageCreate {
	return &discordgo.MessageCreate{Message: message("G1", "<@BOT> "+text)}
}

func Test_New(t *testing.T) {
	t.Setenv("CHATOPS_DISCORD_TEST_TOKEN", "abc")
	a, err := New(config.ChatBackend{Type: config.ChatDiscord, BotTokenEnv: "CHATOPS_DISCORD_TEST_TOKEN"}, nil)
	require.NoError(t, err)
	require.NotNil(t, a.gateway)
	require.False(t, a.Connected())

	_, err = New(config.ChatBackend{Type: config.ChatDiscord, BotTokenEnv: "CHATOPS_DISCORD_TEST_UNSET"}, nil)
	require.ErrorContains(t, err, "discord bot token")
}

func Test_Adapter_Run_open_error(t *testing.T) {
	gw := newFakeGateway()
	gw.openErr <- errors.New("authentication failed")
	_, done, _ := running(t, gw, func(conversation.Message) error { return nil })
	err := next(t, done)
	require.EqualError(t, err, "discord: open gateway: authentication failed")
	require.True(t, gw.closed.Load())
}

func Test_Adapter_Run(t *testing.T) {
	gw := newFakeGateway()
	gw.closeErr = errors.New("already closed")
	gw.threads = map[string]bool{"T1": true}
	submitted := make(chan conversation.Message, 4)
	a, done, cancel := running(t, gw, func(m conversation.Message) error { submitted <- m; return nil })

	gw.events <- &discordgo.Ready{User: &discordgo.User{ID: "BOT"}}
	gw.openErr <- nil
	gw.events <- &discordgo.Connect{}
	require.Eventually(t, a.Connected, 5*time.Second, 10*time.Millisecond)

	gw.events <- &discordgo.MessageCreate{Message: message("G1", "no mention")}
	gw.events <- mention("is it up?")
	msg := next(t, submitted)
	require.Equal(t, "discord:G1:C1:M1", msg.Conversation)
	require.Equal(t, "U1", msg.User)
	require.Equal(t, "is it up?", msg.Text)

	require.NoError(t, msg.Reply(context.Background(), conversation.Reply{Text: "up", Footer: "total 1s"}))
	got := gw.nextSend(t)
	require.Equal(t, "C1", got.channel)
	require.Equal(t, "up\n\n-# total 1s", got.msg.Content)
	require.Equal(t, discordgo.MessageFlagsSuppressEmbeds, got.msg.Flags)
	require.Equal(t, &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}, got.msg.AllowedMentions)
	require.Equal(t, "M1", got.msg.Reference.MessageID)
	require.False(t, *got.msg.Reference.FailIfNotExists)

	inThread := message("G1", "<@BOT> again")
	inThread.ChannelID = "T1"
	gw.events <- &discordgo.MessageCreate{Message: inThread}
	require.Equal(t, "discord:G1:T1", next(t, submitted).Conversation)

	gw.events <- &discordgo.Disconnect{}
	require.Eventually(t, func() bool { return !a.Connected() }, 5*time.Second, 10*time.Millisecond)
	gw.events <- &discordgo.Resumed{}
	require.Eventually(t, a.Connected, 5*time.Second, 10*time.Millisecond)

	cancel()
	require.NoError(t, next(t, done))
	require.False(t, a.Connected())
	require.True(t, gw.closed.Load())
}

func Test_Adapter_Run_cancel_during_open(t *testing.T) {
	gw := newFakeGateway()
	_, done, cancel := running(t, gw, func(conversation.Message) error { return nil })
	cancel()
	// Open still delivers READY while shutdown waits for it; that must not block.
	gw.events <- &discordgo.Ready{User: &discordgo.User{ID: "BOT"}}
	select {
	case err := <-done:
		t.Fatalf("Run returned before Open did: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	require.False(t, gw.closed.Load())

	gw.openErr <- nil
	require.NoError(t, next(t, done))
	require.True(t, gw.closed.Load())
	require.False(t, gw.closedEarly.Load())
}

func Test_Adapter_Run_thread_lookup_error(t *testing.T) {
	gw := newFakeGateway()
	gw.lookupErr = errors.New("missing access")
	submitted := make(chan conversation.Message, 1)
	_, done, cancel := running(t, gw, func(m conversation.Message) error { submitted <- m; return nil })
	gw.openErr <- nil
	gw.events <- &discordgo.Ready{User: &discordgo.User{ID: "BOT"}}
	gw.events <- mention("status")
	require.Equal(t, "discord:G1:C1:M1", next(t, submitted).Conversation)
	cancel()
	require.NoError(t, next(t, done))
}

func Test_Adapter_Run_ready_without_user(t *testing.T) {
	gw := newFakeGateway()
	submitted := make(chan conversation.Message, 1)
	gw.openErr <- nil
	a, done, cancel := running(t, gw, func(m conversation.Message) error { submitted <- m; return nil })
	gw.events <- &discordgo.Ready{}
	require.Eventually(t, a.Connected, 5*time.Second, 10*time.Millisecond)
	gw.events <- &discordgo.MessageCreate{Message: message("", "status")}
	cancel()
	require.NoError(t, next(t, done))
	require.Empty(t, submitted)
}

func Test_Adapter_submit_refused(t *testing.T) {
	tests := map[string]struct {
		err      error
		wantBusy bool
	}{
		"busy":    {err: conversation.ErrBusy, wantBusy: true},
		"dropped": {err: conversation.ErrClosed},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			gw := newFakeGateway()
			gw.openErr <- nil
			a, done, cancel := running(t, gw, func(conversation.Message) error { return tc.err })
			gw.events <- &discordgo.Ready{User: &discordgo.User{ID: "BOT"}}
			gw.events <- mention("status")
			if tc.wantBusy {
				got := gw.nextSend(t)
				require.Equal(t, busyReply, got.msg.Content)
				require.Equal(t, "M1", got.msg.Reference.MessageID)
			}
			cancel()
			require.NoError(t, next(t, done))
			require.False(t, a.Connected())
			if !tc.wantBusy {
				require.Empty(t, gw.sends)
			}
		})
	}
}

func Test_Adapter_replyBusy(t *testing.T) {
	gw := newFakeGateway()
	a := newAdapter(gw, nil)
	for range maxBusyReplies {
		a.busySlots <- struct{}{}
	}
	a.replyBusy(context.Background(), a.logger, a.replier("C1", nil))
	a.busy.Wait()
	require.Empty(t, gw.sends)

	for range maxBusyReplies {
		<-a.busySlots
	}
	gw.setSendErr(errors.New("rate limited"))
	a.replyBusy(context.Background(), a.logger, a.replier("C1", nil))
	a.busy.Wait()
	require.Equal(t, busyReply, gw.nextSend(t).msg.Content)
	require.Empty(t, a.busySlots)
}

func Test_Adapter_replier(t *testing.T) {
	gw := newFakeGateway()
	a := newAdapter(gw, nil)
	ref := &discordgo.MessageReference{MessageID: "M1"}
	text := strings.Repeat("x", 999) + "\n" + strings.Repeat("y", 999)
	require.NoError(t, a.replier("C1", ref)(context.Background(), conversation.Reply{Text: text}))
	first, second := gw.nextSend(t), gw.nextSend(t)
	require.Same(t, ref, first.msg.Reference)
	require.Nil(t, second.msg.Reference)
	require.Empty(t, gw.sends)

	gw.setSendErr(errors.New("missing permissions"))
	err := a.replier("C1", ref)(context.Background(), conversation.Reply{Text: text})
	require.EqualError(t, err, "discord: send reply: missing permissions")
	gw.nextSend(t)
	require.Empty(t, gw.sends)

	require.NoError(t, a.replier("C1", ref)(context.Background(), conversation.Reply{}))
	require.Empty(t, gw.sends)
}
