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
)

// recorder logs observer events as text and checks that waits never exceed the full response time.
type recorder struct {
	mu     sync.Mutex
	events []string
	queued time.Duration
}

func (r *recorder) add(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, fmt.Sprintf(format, args...))
}

func (r *recorder) Received(backend string)         { r.add("received %s", backend) }
func (r *recorder) Rejected(backend, reason string) { r.add("rejected %s %s", backend, reason) }
func (r *recorder) ReplyFailed(backend string)      { r.add("reply-failed %s", backend) }

func (r *recorder) Started(backend string, queued time.Duration) {
	r.mu.Lock()
	r.queued = queued
	r.mu.Unlock()
	r.add("started %s", backend)
}

func (r *recorder) Finished(backend string, stats agent.Stats) {
	r.add("finished %s %s", backend, stats.Outcome)
}

func (r *recorder) Delivered(backend string, outcome agent.Outcome, elapsed time.Duration) {
	r.mu.Lock()
	ok := elapsed >= r.queued && r.queued >= 0
	r.mu.Unlock()
	r.add("delivered %s %s %t", backend, outcome, ok)
}

func (r *recorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func Test_Manager_observer(t *testing.T) {
	answered := agent.Stats{Outcome: agent.OutcomeAnswered}
	tests := map[string]struct {
		runner   statsRunner
		replyErr error
		want     []string
	}{
		"delivered": {
			runner: statsRunner{stats: answered},
			want:   []string{"received slack", "started slack", "finished slack answered", "delivered slack answered true"},
		},
		"failed-turn": {
			runner: statsRunner{stats: agent.Stats{Outcome: agent.OutcomeTimeout}, err: fmt.Errorf("turn: %w", context.DeadlineExceeded)},
			want:   []string{"received slack", "started slack", "finished slack timeout", "delivered slack timeout true"},
		},
		"reply-failed": {
			runner:   statsRunner{stats: answered},
			replyErr: errors.New("slack down"),
			want:     []string{"received slack", "started slack", "finished slack answered", "reply-failed slack"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			rec := &recorder{}
			ctx, cancel := context.WithCancel(context.Background())
			m := New(ctx, tc.runner, testLimits(), nil, rec)
			t.Cleanup(func() { cancel(); m.Wait() })
			out := newReplies()
			out.err = tc.replyErr

			msg := out.message("a", "ping")
			msg.Backend = "slack"
			require.NoError(t, m.Submit(msg))
			out.wait(t)
			m.Shutdown(5 * time.Second)
			require.Equal(t, tc.want, rec.snapshot())
		})
	}
}

func Test_Manager_observer_rejected(t *testing.T) {
	rec := &recorder{}
	runner, out := newFakeRunner(), newReplies()
	limits := testLimits()
	limits.MaxPendingMessages = 1
	ctx, cancel := context.WithCancel(context.Background())
	m := New(ctx, runner, limits, nil, rec)
	t.Cleanup(func() { cancel(); m.Wait() })
	backend := func(msg Message) Message { msg.Backend = "unix"; return msg }

	require.NoError(t, m.Submit(backend(out.message("a", "running"))))
	first := runner.next(t)
	require.ErrorIs(t, m.Submit(backend(out.message("b", "refused"))), ErrBusy)
	first.release <- nil
	out.wait(t)
	m.Shutdown(5 * time.Second)
	require.ErrorIs(t, m.Submit(backend(out.message("c", "late"))), ErrClosed)

	require.Equal(t, []string{
		"received unix", "started unix",
		"received unix", "rejected unix busy",
		"finished unix answered", "delivered unix answered true",
		"received unix", "rejected unix closed",
	}, rec.snapshot())
}

func Test_Manager_gauges(t *testing.T) {
	runner, out := newFakeRunner(), newReplies()
	m, _ := start(t, runner, testLimits())
	require.Zero(t, m.Pending())
	require.Zero(t, m.InFlight())

	require.NoError(t, m.Submit(out.message("a", "running")))
	require.NoError(t, m.Submit(out.message("a", "queued")))
	first := runner.next(t)
	require.Equal(t, 2, m.Pending())
	require.Equal(t, 1, m.InFlight())

	first.release <- nil
	out.wait(t)
	runner.next(t).release <- nil
	out.wait(t)
	require.Eventually(t, func() bool { return m.Pending() == 0 && m.InFlight() == 0 }, 5*time.Second, 10*time.Millisecond)
}
