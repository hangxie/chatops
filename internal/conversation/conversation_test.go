package conversation

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/internal/agent"
	"github.com/hangxie/chatops/internal/config"
	"github.com/hangxie/chatops/internal/llm"
)

// call is one Run invocation seen by fakeRunner; release lets it finish.
type call struct {
	input   string
	history []llm.Message
	ctx     context.Context
	release chan error
}

// fakeRunner blocks every turn until the test releases it.
type fakeRunner struct {
	calls chan *call
}

func newFakeRunner() *fakeRunner { return &fakeRunner{calls: make(chan *call, 16)} }

func (r *fakeRunner) Run(ctx context.Context, history []llm.Message, input string) (agent.Turn, error) {
	c := &call{input: input, history: history, ctx: ctx, release: make(chan error, 1)}
	r.calls <- c
	select {
	case err := <-c.release:
		if err != nil {
			return agent.Turn{}, err
		}
	case <-ctx.Done():
		return agent.Turn{}, ctx.Err()
	}
	reply := "re: " + input
	return agent.Turn{Reply: reply, Messages: []llm.Message{
		{Role: llm.RoleUser, Content: input},
		{Role: llm.RoleAssistant, Content: reply},
	}}, nil
}

func (r *fakeRunner) next(t *testing.T) *call {
	t.Helper()
	select {
	case c := <-r.calls:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a turn to start")
		return nil
	}
}

func (r *fakeRunner) idle(t *testing.T) {
	t.Helper()
	select {
	case c := <-r.calls:
		t.Fatalf("unexpected turn started: %q", c.input)
	case <-time.After(50 * time.Millisecond):
	}
}

// replies collects every Reply call, keyed by conversation.
type replies struct {
	mu   sync.Mutex
	got  map[string][]string
	sent chan string
	err  error
	// hold, when set, keeps every Reply call blocked until it is closed.
	hold chan struct{}
}

func newReplies() *replies { return &replies{got: map[string][]string{}, sent: make(chan string, 16)} }

func (r *replies) message(key, text string) Message {
	return Message{Conversation: key, User: "U1", Text: text, Reply: func(ctx context.Context, reply string) error {
		r.mu.Lock()
		r.got[key] = append(r.got[key], reply)
		r.mu.Unlock()
		r.sent <- reply
		if r.hold != nil {
			select {
			case <-r.hold:
			case <-ctx.Done():
			}
		}
		return r.err
	}}
}

func (r *replies) wait(t *testing.T) string {
	t.Helper()
	select {
	case reply := <-r.sent:
		return reply
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a reply")
		return ""
	}
}

func testLimits() config.Agent {
	return config.Agent{HistoryTurns: 10, HistoryTTL: time.Hour, MaxConcurrentTurns: 4, MaxPendingMessages: 16}
}

func start(t *testing.T, runner Runner, limits config.Agent) (*Manager, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	m := New(ctx, runner, limits, nil)
	t.Cleanup(func() {
		cancel()
		m.Wait()
	})
	return m, cancel
}

func turnMessages(inputs ...string) []llm.Message {
	var messages []llm.Message
	for _, input := range inputs {
		messages = append(messages,
			llm.Message{Role: llm.RoleUser, Content: input},
			llm.Message{Role: llm.RoleAssistant, Content: "re: " + input})
	}
	return messages
}

func Test_Manager_serializes_a_conversation(t *testing.T) {
	runner, out := newFakeRunner(), newReplies()
	m, _ := start(t, runner, testLimits())

	require.NoError(t, m.Submit(out.message("a", "one")))
	require.NoError(t, m.Submit(out.message("a", "two")))
	first := runner.next(t)
	require.Equal(t, "one", first.input)
	runner.idle(t)

	first.release <- nil
	require.Equal(t, "re: one", out.wait(t))
	second := runner.next(t)
	require.Equal(t, "two", second.input)
	require.Equal(t, turnMessages("one"), second.history)
	second.release <- nil
	require.Equal(t, "re: two", out.wait(t))
}

func Test_Manager_runs_conversations_concurrently_without_sharing_history(t *testing.T) {
	runner, out := newFakeRunner(), newReplies()
	m, _ := start(t, runner, testLimits())

	require.NoError(t, m.Submit(out.message("a", "a1")))
	runner.next(t).release <- nil
	out.wait(t)

	require.NoError(t, m.Submit(out.message("a", "a2")))
	require.NoError(t, m.Submit(out.message("b", "b1")))
	calls := map[string]*call{}
	for range 2 {
		c := runner.next(t)
		calls[c.input] = c
	}
	require.Equal(t, turnMessages("a1"), calls["a2"].history)
	require.Empty(t, calls["b1"].history)
	calls["a2"].release <- nil
	calls["b1"].release <- nil
	out.wait(t)
	out.wait(t)
}

func Test_Manager_limits_concurrent_turns(t *testing.T) {
	runner, out := newFakeRunner(), newReplies()
	limits := testLimits()
	limits.MaxConcurrentTurns = 1
	m, _ := start(t, runner, limits)

	require.NoError(t, m.Submit(out.message("a", "a1")))
	require.NoError(t, m.Submit(out.message("b", "b1")))
	first := runner.next(t)
	runner.idle(t)
	first.release <- nil
	runner.next(t).release <- nil
	out.wait(t)
	out.wait(t)
}

func Test_Manager_reply_delivery_does_not_hold_a_turn_slot(t *testing.T) {
	runner, out := newFakeRunner(), newReplies()
	out.hold = make(chan struct{})
	limits := testLimits()
	limits.MaxConcurrentTurns = 1
	m, _ := start(t, runner, limits)

	require.NoError(t, m.Submit(out.message("a", "a1")))
	require.NoError(t, m.Submit(out.message("a", "a2")))
	runner.next(t).release <- nil
	require.Equal(t, "re: a1", out.wait(t))

	require.NoError(t, m.Submit(out.message("b", "b1")))
	other := runner.next(t)
	require.Equal(t, "b1", other.input, "another conversation runs while a1's reply is still being delivered")
	runner.idle(t)

	close(out.hold)
	other.release <- nil
	require.Equal(t, "re: b1", out.wait(t))
	next := runner.next(t)
	require.Equal(t, "a2", next.input, "a2 waits for a1's reply")
	next.release <- nil
	out.wait(t)
}

func Test_Manager_history_bounds(t *testing.T) {
	tests := map[string]struct {
		turns   int
		advance time.Duration
		want    []llm.Message
	}{
		"keeps-recent-turns": {turns: 2, want: turnMessages("t2", "t3")},
		"expires-after-ttl":  {turns: 10, advance: time.Hour},
		"kept-within-ttl":    {turns: 10, advance: time.Hour - time.Second, want: turnMessages("t1", "t2", "t3")},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			runner, out := newFakeRunner(), newReplies()
			limits := testLimits()
			limits.HistoryTurns = tc.turns
			m, _ := start(t, runner, limits)
			now := time.Unix(1000, 0)
			m.now = func() time.Time { return now }

			for _, input := range []string{"t1", "t2", "t3"} {
				require.NoError(t, m.Submit(out.message("a", input)))
				runner.next(t).release <- nil
				out.wait(t)
			}
			now = now.Add(tc.advance)
			require.NoError(t, m.Submit(out.message("a", "t4")))
			last := runner.next(t)
			require.Equal(t, tc.want, last.history)
			last.release <- nil
			out.wait(t)
		})
	}
}

func Test_Manager_history_disabled(t *testing.T) {
	runner, out := newFakeRunner(), newReplies()
	limits := testLimits()
	limits.HistoryTurns = 0
	m, _ := start(t, runner, limits)

	for _, input := range []string{"t1", "t2", "t3"} {
		require.NoError(t, m.Submit(out.message("a", input)))
		c := runner.next(t)
		require.Nil(t, c.history, "earlier turns are never replayed when history is disabled")
		c.release <- nil
		out.wait(t)
	}
}

func Test_Manager_failed_turn_counts_as_activity(t *testing.T) {
	runner, out := newFakeRunner(), newReplies()
	m, _ := start(t, runner, testLimits())
	now := time.Unix(1000, 0)
	m.now = func() time.Time { return now }

	require.NoError(t, m.Submit(out.message("a", "t1")))
	runner.next(t).release <- nil
	out.wait(t)
	now = now.Add(50 * time.Minute)
	require.NoError(t, m.Submit(out.message("a", "fails")))
	runner.next(t).release <- errors.New("model: 503")
	out.wait(t)

	now = now.Add(50 * time.Minute)
	require.NoError(t, m.Submit(out.message("a", "t3")))
	last := runner.next(t)
	require.Equal(t, turnMessages("t1"), last.history, "idle time counts from the failed attempt, not the last success")
	last.release <- nil
	out.wait(t)
}

func Test_Manager_failed_turn(t *testing.T) {
	tests := map[string]struct {
		err   error
		reply string
	}{
		"timeout": {
			err:   fmt.Errorf("turn exceeded 2m0s: %w", context.DeadlineExceeded),
			reply: "Sorry, I ran out of time on that request. Try a narrower question.",
		},
		"other": {
			err:   errors.New("model: llm: request completion: dial tcp 10.0.0.1:8080: connection refused"),
			reply: "Sorry, I couldn't complete that request; the error has been logged.",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			runner, out := newFakeRunner(), newReplies()
			m, _ := start(t, runner, testLimits())

			require.NoError(t, m.Submit(out.message("a", "fails")))
			runner.next(t).release <- tc.err
			require.Equal(t, tc.reply, out.wait(t))

			require.NoError(t, m.Submit(out.message("a", "next")))
			next := runner.next(t)
			require.Empty(t, next.history, "a failed turn is not kept in history")
			next.release <- nil
			out.wait(t)
		})
	}
}

func Test_Manager_reply_error_does_not_stop_conversation(t *testing.T) {
	runner, out := newFakeRunner(), newReplies()
	out.err = errors.New("slack: channel_not_found")
	m, _ := start(t, runner, testLimits())

	require.NoError(t, m.Submit(out.message("a", "one")))
	require.NoError(t, m.Submit(out.message("a", "two")))
	runner.next(t).release <- nil
	out.wait(t)
	next := runner.next(t)
	require.Equal(t, turnMessages("one"), next.history)
	next.release <- nil
	out.wait(t)
}

func Test_Manager_busy(t *testing.T) {
	runner, out := newFakeRunner(), newReplies()
	limits := testLimits()
	limits.MaxPendingMessages = 2
	m, _ := start(t, runner, limits)

	require.NoError(t, m.Submit(out.message("a", "running")))
	first := runner.next(t)
	require.NoError(t, m.Submit(out.message("a", "queued")))
	require.ErrorIs(t, m.Submit(out.message("b", "refused")), ErrBusy)

	first.release <- nil
	out.wait(t)
	runner.next(t).release <- nil
	out.wait(t)
	require.NoError(t, m.Submit(out.message("b", "accepted")))
	runner.next(t).release <- nil
	out.wait(t)
}

func Test_Manager_shutdown(t *testing.T) {
	runner, out := newFakeRunner(), newReplies()
	ctx, cancel := context.WithCancel(context.Background())
	m := New(ctx, runner, testLimits(), nil)

	require.NoError(t, m.Submit(out.message("a", "in-flight")))
	require.NoError(t, m.Submit(out.message("a", "queued")))
	inFlight := runner.next(t)
	cancel()
	m.Wait()

	require.ErrorIs(t, inFlight.ctx.Err(), context.Canceled)
	runner.idle(t)
	require.Empty(t, out.got, "no reply is posted for a turn cut short by shutdown")
	require.ErrorIs(t, m.Submit(out.message("a", "late")), ErrClosed)
}

func Test_Manager_sweeps_idle_conversations(t *testing.T) {
	runner, out := newFakeRunner(), newReplies()
	m, _ := start(t, runner, testLimits())
	now := time.Unix(1000, 0)
	m.now = func() time.Time { return now }

	require.NoError(t, m.Submit(out.message("old", "x")))
	runner.next(t).release <- nil
	out.wait(t)
	now = now.Add(time.Hour)
	require.NoError(t, m.Submit(out.message("new", "y")))
	running := runner.next(t)

	m.mu.Lock()
	_, kept := m.threads["old"]
	_, active := m.threads["new"]
	m.mu.Unlock()
	require.False(t, kept, "expired idle conversation is dropped")
	require.True(t, active)
	running.release <- nil
	out.wait(t)
}
