package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/chat"
	"github.com/hangxie/chatops/planner"
	"github.com/hangxie/chatops/tool/reply"
)

type fakeConn struct {
	mu           sync.Mutex
	received     []chat.Message
	receiveErr   error
	afterReceive func()
	sent         []chat.Message
	sendErr      error
	closed       int
	closeErr     error
}

func (f *fakeConn) Receive(ctx context.Context) (chat.Message, error) {
	f.mu.Lock()
	if len(f.received) > 0 {
		msg := f.received[0]
		f.received = f.received[1:]
		f.mu.Unlock()
		if f.afterReceive != nil {
			f.afterReceive()
		}
		return msg, nil
	}
	f.mu.Unlock()
	if f.receiveErr != nil {
		return chat.Message{}, f.receiveErr
	}
	<-ctx.Done()
	return chat.Message{}, ctx.Err()
}

func (f *fakeConn) Send(_ context.Context, msg chat.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, msg)
	return nil
}

func (f *fakeConn) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return f.closeErr
}

type fakePlanner struct {
	requests []planner.Request
	plans    []planner.Plan
	err      error
	cancel   context.CancelFunc
	closed   int
	closeErr error
}

func (f *fakePlanner) Plan(_ context.Context, req planner.Request) (planner.Plan, error) {
	f.requests = append(f.requests, req)
	if f.cancel != nil {
		f.cancel()
	}
	if f.err != nil {
		return planner.Plan{}, f.err
	}
	plan := f.plans[0]
	f.plans = f.plans[1:]
	return plan, nil
}

func (f *fakePlanner) Close() error {
	f.closed++
	return f.closeErr
}

func Test_New_validates_dependencies(t *testing.T) {
	conn := &fakeConn{}
	p := &fakePlanner{}
	tools := newHost(t, nil)

	testCases := map[string]struct {
		config Config
		errMsg string
	}{
		"valid":       {config: Config{Chat: conn, Planner: p, Tools: tools}},
		"nil-chat":    {config: Config{Planner: p, Tools: tools}, errMsg: "nil chat"},
		"nil-planner": {config: Config{Chat: conn, Tools: tools}, errMsg: "nil planner"},
		"nil-tools":   {config: Config{Chat: conn, Planner: p}, errMsg: "nil tool host"},
		"negative-concurrency": {
			config: Config{Chat: conn, Planner: p, Tools: tools, MaxConcurrency: -1}, errMsg: "negative maximum concurrency",
		},
		"excessive-concurrency": {
			config: Config{Chat: conn, Planner: p, Tools: tools, MaxConcurrency: MaximumMaxConcurrency + 1}, errMsg: "maximum concurrency exceeds",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			e, err := New(tc.config)
			if tc.errMsg != "" {
				require.Nil(t, e)
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, e)
			require.Equal(t, DefaultMaxConcurrency, e.maxConcurrency)
		})
	}
}

func Test_Run_plans_executes_and_replies(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn := &fakeConn{received: []chat.Message{{ConversationID: "conversation-1", Sender: "alice", Text: "do it"}}}
	p := &fakePlanner{plans: []planner.Plan{{Steps: []planner.Step{
		{Tool: reply.ToolName, Arguments: map[string]any{"text": "working"}},
		{Tool: "fake", Arguments: map[string]any{"unit": "web"}},
	}}}}
	taskTool := &stubTool{name: "fake", text: "restarted web"}
	e, err := New(Config{ConnectionID: "chat-1", Chat: conn, Planner: p, Tools: newHost(t, conn, taskTool)})
	require.NoError(t, err)

	result := make(chan error, 1)
	go func() { result <- e.Run(ctx) }()
	require.Eventually(t, func() bool {
		conn.mu.Lock()
		defer conn.mu.Unlock()
		return len(conn.sent) == 2
	}, time.Second, time.Millisecond)
	cancel()
	require.NoError(t, <-result)

	require.Equal(t, []planner.Request{{
		Text: "do it", ConnectionID: "chat-1", ConversationID: "conversation-1", Sender: "alice",
	}}, p.requests)
	require.Equal(t, []map[string]any{{"unit": "web"}}, taskTool.recorded())
	require.Equal(t, []chat.Message{
		{ConversationID: "conversation-1", Text: "working"},
		{ConversationID: "conversation-1", Text: "restarted web"},
	}, conn.sent)
	require.Equal(t, 1, conn.closed)
	require.Equal(t, 1, p.closed)
}

// planStep builds a single-use planner that returns a one-step plan.
func planStep(toolName string, args map[string]any) *fakePlanner {
	return &fakePlanner{plans: []planner.Plan{{Steps: []planner.Step{{Tool: toolName, Arguments: args}}}}}
}

// sentContains reports whether conn has sent a message with the given text.
func sentContains(conn *fakeConn, text string) bool {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	for _, m := range conn.sent {
		if m.Text == text {
			return true
		}
	}
	return false
}

func Test_Run_receive_error_stops_and_closes(t *testing.T) {
	testErr := errors.New("connection lost")
	conn := &fakeConn{receiveErr: testErr}
	p := &fakePlanner{}
	e, err := New(Config{Chat: conn, Planner: p, Tools: newHost(t, conn)})
	require.NoError(t, err)

	err = e.Run(context.Background())
	require.ErrorIs(t, err, testErr)
	require.ErrorContains(t, err, "receive message")
	require.Equal(t, 1, conn.closed)
	require.Equal(t, 1, p.closed)
}

// Test_Run_message_failure_is_nonfatal verifies that a failure while handling
// one message — a bad plan, an unknown or failing tool, a misconfigured step —
// posts a failure notice to the requester and keeps the engine running instead
// of stopping it.
func Test_Run_message_failure_is_nonfatal(t *testing.T) {
	testErr := errors.New("stage failed")
	testCases := map[string]struct {
		planner *fakePlanner
		invoked *stubTool
	}{
		"plan":         {planner: &fakePlanner{err: testErr}},
		"unknown-tool": {planner: planStep("missing", nil)},
		"call-fails": {
			planner: planStep("fake", nil),
			invoked: &stubTool{name: "fake", toolErr: testErr, rawError: true},
		},
		// A reply with no text cannot be posted; the host tool reports it.
		"reply-without-arguments": {planner: planStep(reply.ToolName, nil)},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			conn := &fakeConn{received: []chat.Message{{ConversationID: "c1", Text: "do it"}}}
			var stubs []*stubTool
			if tc.invoked != nil {
				stubs = append(stubs, tc.invoked)
			}
			e, err := New(Config{Chat: conn, Planner: tc.planner, Tools: newHost(t, conn, stubs...)})
			require.NoError(t, err)

			result := make(chan error, 1)
			go func() { result <- e.Run(ctx) }()
			require.Eventually(t, func() bool { return sentContains(conn, failureNotice) }, time.Second, time.Millisecond)
			cancel()
			require.NoError(t, <-result)

			require.Equal(t, 1, conn.closed)
			require.Equal(t, 1, tc.planner.closed)
		})
	}
}

// Test_Run_relays_tool_reported_error verifies MCP's IsError contract: a tool
// that ran and reported its own failure is not an engine failure, so the
// requester sees what the tool said rather than the generic notice.
func Test_Run_relays_tool_reported_error(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn := &fakeConn{received: []chat.Message{{ConversationID: "c1", Text: "do it"}}}
	taskTool := &stubTool{name: "fake", toolErr: errors.New("cluster unreachable")}
	p := planStep("fake", nil)
	e, err := New(Config{Chat: conn, Planner: p, Tools: newHost(t, conn, taskTool)})
	require.NoError(t, err)

	result := make(chan error, 1)
	go func() { result <- e.Run(ctx) }()
	require.Eventually(t, func() bool { return sentContains(conn, "cluster unreachable") }, time.Second, time.Millisecond)
	cancel()
	require.NoError(t, <-result)
	require.False(t, sentContains(conn, failureNotice))
}

// Test_Run_survives_send_failure verifies that when even the reply cannot be
// delivered (so the failure notice cannot either), the engine still keeps
// running rather than stopping.
func Test_Run_survives_send_failure(t *testing.T) {
	testErr := errors.New("send failed")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn := &fakeConn{received: []chat.Message{{ConversationID: "c1"}}, sendErr: testErr}
	invoked := &stubTool{name: "fake", text: "done"}
	p := planStep("fake", nil)
	e, err := New(Config{Chat: conn, Planner: p, Tools: newHost(t, conn, invoked)})
	require.NoError(t, err)

	result := make(chan error, 1)
	go func() { result <- e.Run(ctx) }()
	require.Eventually(t, func() bool { return invoked.callCount() == 1 }, time.Second, time.Millisecond)
	cancel()
	require.NoError(t, <-result)
}

// Test_Run_survives_panicking_tool verifies that a tool panicking inside its
// server does not take the engine down. The panic now happens across the MCP
// boundary rather than in the engine's own goroutine, so the engine sees a
// failed call and notifies the requester.
// panickingPlanner panics instead of planning.
type panickingPlanner struct{ closed int }

func (p *panickingPlanner) Plan(context.Context, planner.Request) (planner.Plan, error) {
	panic("planner boom")
}

func (p *panickingPlanner) Close() error {
	p.closed++
	return nil
}

func Test_Run_recovers_panicking_planner_and_continues(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn := &fakeConn{received: []chat.Message{{ConversationID: "c1"}}}
	p := &panickingPlanner{}
	e, err := New(Config{Chat: conn, Planner: p, Tools: newHost(t, conn)})
	require.NoError(t, err)

	result := make(chan error, 1)
	go func() { result <- e.Run(ctx) }()
	// The planner runs in this process, so its panic is recovered here; the
	// requester is notified and the engine keeps serving.
	require.Eventually(t, func() bool { return sentContains(conn, failureNotice) }, time.Second, time.Millisecond)
	cancel()
	require.NoError(t, <-result)
	require.Equal(t, 1, p.closed)
}

func Test_Run_survives_panicking_tool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn := &fakeConn{received: []chat.Message{{ConversationID: "c1"}}}
	p := planStep("panicky", nil)
	taskTool := &stubTool{name: "panicky", panics: true}
	e, err := New(Config{Chat: conn, Planner: p, Tools: newHost(t, conn, taskTool)})
	require.NoError(t, err)

	result := make(chan error, 1)
	go func() { result <- e.Run(ctx) }()
	require.Eventually(t, func() bool { return sentContains(conn, failureNotice) }, time.Second, time.Millisecond)
	cancel()
	require.NoError(t, <-result)
	require.Equal(t, 1, conn.closed)
	require.Equal(t, 1, p.closed)
}

// Test_Run_abandons_the_plan_after_a_failed_step: a later step may well have
// assumed an earlier one worked, so the rest of the plan is dropped rather
// than run against a state that never came about.
func Test_Run_abandons_the_plan_after_a_failed_step(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn := &fakeConn{received: []chat.Message{{ConversationID: "c1"}}}
	failing := &stubTool{name: "failing", toolErr: errors.New("cluster unreachable")}
	after := &stubTool{name: "after", text: "should not run"}
	p := &fakePlanner{plans: []planner.Plan{{Steps: []planner.Step{
		{Tool: "failing"},
		{Tool: "after"},
	}}}}
	e, err := New(Config{Chat: conn, Planner: p, Tools: newHost(t, conn, failing, after)})
	require.NoError(t, err)

	result := make(chan error, 1)
	go func() { result <- e.Run(ctx) }()
	require.Eventually(t, func() bool { return sentContains(conn, "cluster unreachable") }, time.Second, time.Millisecond)
	cancel()
	require.NoError(t, <-result)

	require.Equal(t, 1, failing.callCount())
	require.Zero(t, after.callCount(), "the plan continued past a failed step")
	require.False(t, sentContains(conn, "should not run"))
}

// Test_Run_notifies_on_a_silent_tool_failure: an external server may report a
// failure with no content at all, and the requester must not be left hearing
// nothing.
func Test_Run_notifies_on_a_silent_tool_failure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn := &fakeConn{received: []chat.Message{{ConversationID: "c1"}}}
	silent := &stubTool{name: "silent", silentError: true}
	p := planStep("silent", nil)
	e, err := New(Config{Chat: conn, Planner: p, Tools: newHost(t, conn, silent)})
	require.NoError(t, err)

	result := make(chan error, 1)
	go func() { result <- e.Run(ctx) }()
	require.Eventually(t, func() bool { return sentContains(conn, failureNotice) }, time.Second, time.Millisecond)
	cancel()
	require.NoError(t, <-result)
}

func Test_Run_chat_closed_mid_burst_is_graceful(t *testing.T) {
	// A burst keeps the run loop submitting while a worker is failing, so the
	// stop is observed by whichever of Submit and Done gets there first. Both
	// must report the same graceful outcome.
	msgs := make([]chat.Message, 8)
	for i := range msgs {
		msgs[i] = chat.Message{ConversationID: fmt.Sprintf("c%d", i)}
	}
	conn := &fakeConn{
		received: msgs,
		sendErr:  fmt.Errorf("fake: %w", chat.ErrClosed),
		// Trickle the messages in so the first one has failed, and the
		// scheduler stopped, while the run loop is still submitting.
		afterReceive: func() { time.Sleep(10 * time.Millisecond) },
	}
	taskTool := &stubTool{name: "fake", text: "done"}
	p := &fakePlanner{plans: make([]planner.Plan, len(msgs))}
	for i := range p.plans {
		p.plans[i] = planner.Plan{Steps: []planner.Step{{Tool: "fake"}}}
	}
	e, err := New(Config{Chat: conn, Planner: p, Tools: newHost(t, conn, taskTool), MaxConcurrency: 4})
	require.NoError(t, err)

	require.NoError(t, e.Run(context.Background()))
	require.Equal(t, 1, conn.closed)
}

func Test_Run_context_cancellation_during_planning_is_graceful(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	conn := &fakeConn{received: []chat.Message{{ConversationID: "c1"}}}
	p := &fakePlanner{err: context.Canceled, cancel: cancel}
	e, err := New(Config{Chat: conn, Planner: p, Tools: newHost(t, conn)})
	require.NoError(t, err)
	require.NoError(t, e.Run(ctx))
}

func Test_Run_context_deadline_is_graceful(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	conn := &fakeConn{}
	p := &fakePlanner{}
	e, err := New(Config{Chat: conn, Planner: p, Tools: newHost(t, conn)})
	require.NoError(t, err)
	// The chat has no messages, so the run ends when the context deadline
	// fires, which is a graceful stop.
	require.NoError(t, e.Run(ctx))
}

func Test_Run_preserves_receive_error_that_races_with_cancellation(t *testing.T) {
	testErr := errors.New("backend failed")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	conn := &fakeConn{receiveErr: testErr}
	p := &fakePlanner{}
	e, err := New(Config{Chat: conn, Planner: p, Tools: newHost(t, conn)})
	require.NoError(t, err)
	// A fatal receive error is reported even though it races with
	// cancellation; per-message errors, by contrast, are non-fatal and
	// covered by Test_Run_message_failure_is_nonfatal.
	require.ErrorIs(t, e.Run(ctx), testErr)
}

func Test_Run_closed_chat_is_graceful(t *testing.T) {
	conn := &fakeConn{receiveErr: fmt.Errorf("fake: %w", chat.ErrClosed)}
	p := &fakePlanner{}
	e, err := New(Config{Chat: conn, Planner: p, Tools: newHost(t, conn)})
	require.NoError(t, err)
	require.NoError(t, e.Run(context.Background()))
}

func Test_Run_chat_closed_while_handling_is_graceful(t *testing.T) {
	conn := &fakeConn{
		received: []chat.Message{{ConversationID: "c1"}},
		sendErr:  fmt.Errorf("fake: %w", chat.ErrClosed),
	}
	p := planStep("fake", nil)
	taskTool := &stubTool{name: "fake", text: "done"}
	e, err := New(Config{Chat: conn, Planner: p, Tools: newHost(t, conn, taskTool)})
	require.NoError(t, err)
	require.NoError(t, e.Run(context.Background()))
}

func Test_Run_binds_reply_to_originating_conversation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	conn := &fakeConn{received: []chat.Message{{ConversationID: "origin"}}}
	// Hold a reference to the planner's argument map so the test can prove the
	// engine did not mutate it while injecting the conversation.
	replyArgs := map[string]any{"text": "hello"}
	// The plan names no conversation: the engine carries it on the context,
	// so the reply lands on "origin" and the model never chooses a target.
	p := &fakePlanner{plans: []planner.Plan{{Steps: []planner.Step{
		{Tool: reply.ToolName, Arguments: replyArgs},
	}}}}
	e, err := New(Config{Chat: conn, Planner: p, Tools: newHost(t, conn)})
	require.NoError(t, err)
	result := make(chan error, 1)
	go func() { result <- e.Run(ctx) }()
	require.Eventually(t, func() bool {
		conn.mu.Lock()
		defer conn.mu.Unlock()
		return len(conn.sent) == 1
	}, time.Second, time.Millisecond)
	cancel()
	require.NoError(t, <-result)
	require.Equal(t, "origin", conn.sent[0].ConversationID)

	// The engine passes the planner's map through untouched; nothing is
	// injected into it, so a concurrent-safe planner may share it.
	require.Equal(t, map[string]any{"text": "hello"}, replyArgs)
}

type parallelPlanner struct {
	aStarted chan struct{}
	aRelease chan struct{}
	bDone    chan struct{}
}

func (p *parallelPlanner) Plan(_ context.Context, req planner.Request) (planner.Plan, error) {
	switch req.ConversationID {
	case "a":
		close(p.aStarted)
		<-p.aRelease
	case "b":
		close(p.bDone)
	}
	return planner.Plan{}, nil
}

func (p *parallelPlanner) Close() error { return nil }

func Test_Run_processes_conversations_concurrently(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn := &fakeConn{received: []chat.Message{{ConversationID: "a"}, {ConversationID: "b"}}}
	p := &parallelPlanner{aStarted: make(chan struct{}), aRelease: make(chan struct{}), bDone: make(chan struct{})}
	e, err := New(Config{Chat: conn, Planner: p, Tools: newHost(t, conn), MaxConcurrency: 2})
	require.NoError(t, err)
	result := make(chan error, 1)
	go func() { result <- e.Run(ctx) }()
	<-p.aStarted
	require.Eventually(t, func() bool {
		select {
		case <-p.bDone:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)
	close(p.aRelease)
	cancel()
	require.NoError(t, <-result)
}

type blockingPlanner struct {
	started chan struct{}
	release chan struct{}
	closed  atomic.Bool
}

func (p *blockingPlanner) Plan(context.Context, planner.Request) (planner.Plan, error) {
	close(p.started)
	<-p.release
	return planner.Plan{}, nil
}

func (p *blockingPlanner) Close() error {
	p.closed.Store(true)
	return nil
}

func Test_Close_waits_for_in_flight_plan(t *testing.T) {
	conn := &fakeConn{received: []chat.Message{{ConversationID: "c1"}}}
	p := &blockingPlanner{started: make(chan struct{}), release: make(chan struct{})}
	e, err := New(Config{Chat: conn, Planner: p, Tools: newHost(t, conn)})
	require.NoError(t, err)
	runResult := make(chan error, 1)
	go func() { runResult <- e.Run(context.Background()) }()
	<-p.started

	closeResult := make(chan error, 1)
	go func() { closeResult <- e.Close() }()
	require.Never(t, p.closed.Load, 50*time.Millisecond, time.Millisecond)
	close(p.release)
	require.NoError(t, <-closeResult)
	require.NoError(t, <-runResult)
	require.True(t, p.closed.Load())
}

func Test_Run_does_not_start_work_after_Close(t *testing.T) {
	conn := &fakeConn{received: []chat.Message{{ConversationID: "c1"}}}
	p := &fakePlanner{}
	e, err := New(Config{Chat: conn, Planner: p, Tools: newHost(t, conn)})
	require.NoError(t, err)
	conn.afterReceive = func() { require.NoError(t, e.Close()) }
	require.NoError(t, e.Run(context.Background()))
	require.Empty(t, p.requests)
}

func Test_processMessage_after_Close(t *testing.T) {
	conn := &fakeConn{}
	p := &fakePlanner{}
	e, err := New(Config{Chat: conn, Planner: p, Tools: newHost(t, conn)})
	require.NoError(t, err)
	require.NoError(t, e.Close())
	require.ErrorIs(t, e.processMessage(context.Background(), chat.Message{}), context.Canceled)
}

func Test_receiveLoop_cancellation_while_delivering_message(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	conn := &fakeConn{received: []chat.Message{{ConversationID: "c1"}}}
	e := &Engine{chat: conn}
	messages := make(chan chat.Message)
	receiveErrors := make(chan error, 1)
	go e.receiveLoop(ctx, messages, receiveErrors)
	cancel()
	require.ErrorIs(t, <-receiveErrors, context.Canceled)
}

func Test_Close_is_idempotent_and_joins_errors(t *testing.T) {
	chatErr := errors.New("chat close")
	plannerErr := errors.New("planner close")
	conn := &fakeConn{closeErr: chatErr}
	p := &fakePlanner{closeErr: plannerErr}
	e, err := New(Config{Chat: conn, Planner: p, Tools: newHost(t, conn)})
	require.NoError(t, err)

	err = e.Close()
	require.ErrorIs(t, err, chatErr)
	require.ErrorIs(t, err, plannerErr)
	require.Equal(t, 1, conn.closed)
	require.Equal(t, 1, p.closed)
	require.Equal(t, err, e.Close())
}

func Test_Run_rejects_second_run(t *testing.T) {
	conn := &fakeConn{}
	p := &fakePlanner{}
	e, err := New(Config{Chat: conn, Planner: p, Tools: newHost(t, conn)})
	require.NoError(t, err)
	require.NoError(t, e.Close())

	err = e.Run(context.Background())
	require.ErrorContains(t, err, "already closed")
}

func Test_Run_rejects_concurrent_run(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	conn := &fakeConn{}
	p := &fakePlanner{}
	e, err := New(Config{Chat: conn, Planner: p, Tools: newHost(t, conn)})
	require.NoError(t, err)
	result := make(chan error, 1)
	go func() { result <- e.Run(ctx) }()

	require.Eventually(t, func() bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.run
	}, time.Second, time.Millisecond)
	require.ErrorContains(t, e.Run(context.Background()), "already running")
	cancel()
	require.NoError(t, <-result)
}
