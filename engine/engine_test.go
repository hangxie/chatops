package engine

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/chat"
	"github.com/hangxie/chatops/cred"
	"github.com/hangxie/chatops/planner"
	"github.com/hangxie/chatops/tool"
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

type fakeTool struct {
	calls    []tool.Call
	result   tool.Result
	err      error
	closed   int
	closeErr error
}

func (f *fakeTool) Invoke(_ context.Context, call tool.Call) (tool.Result, error) {
	f.calls = append(f.calls, call)
	return f.result, f.err
}

func (f *fakeTool) Close() error {
	f.closed++
	return f.closeErr
}

func Test_New_validates_dependencies(t *testing.T) {
	conn := &fakeConn{}
	p := &fakePlanner{}
	tools := tool.NewRegistry()

	testCases := map[string]struct {
		config Config
		errMsg string
	}{
		"valid":       {config: Config{Chat: conn, Planner: p, Tools: tools}},
		"nil-chat":    {config: Config{Planner: p, Tools: tools}, errMsg: "nil chat"},
		"nil-planner": {config: Config{Chat: conn, Tools: tools}, errMsg: "nil planner"},
		"nil-tools":   {config: Config{Chat: conn, Planner: p}, errMsg: "nil tool registry"},
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
		{Tool: "reply://", Call: tool.Call{Action: "send", Target: "conversation-1", Parameters: map[string]string{"text": "working"}}},
		{Tool: "fake://", Call: tool.Call{Action: "restart", Target: "web"}},
	}}}}
	taskTool := &fakeTool{result: tool.Result{Text: "restarted web"}}
	tools := tool.NewRegistry(tool.Backend{Scheme: "fake", Opener: func(_ context.Context, _ *url.URL, _ cred.Store) (tool.Tool, error) {
		return taskTool, nil
	}})
	e, err := New(Config{ConnectionID: "chat-1", Chat: conn, Planner: p, Tools: tools})
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
	require.Equal(t, []tool.Call{{Action: "restart", Target: "web"}}, taskTool.calls)
	require.Equal(t, []chat.Message{
		{ConversationID: "conversation-1", Text: "working"},
		{ConversationID: "conversation-1", Text: "restarted web"},
	}, conn.sent)
	require.Equal(t, 1, taskTool.closed)
	require.Equal(t, 1, conn.closed)
	require.Equal(t, 1, p.closed)
}

func Test_Run_reports_stage_errors_and_closes(t *testing.T) {
	testErr := errors.New("stage failed")
	testCases := map[string]struct {
		conn        *fakeConn
		planner     *fakePlanner
		tools       *tool.Registry
		invoked     *fakeTool
		errContains string
	}{
		"receive": {
			conn: &fakeConn{receiveErr: testErr}, planner: &fakePlanner{}, tools: tool.NewRegistry(), errContains: "receive message",
		},
		"plan": {
			conn:    &fakeConn{received: []chat.Message{{ConversationID: "c1", Text: "hello"}}},
			planner: &fakePlanner{err: testErr}, tools: tool.NewRegistry(), errContains: "plan message",
		},
		"open-tool": {
			conn:    &fakeConn{received: []chat.Message{{ConversationID: "c1"}}},
			planner: &fakePlanner{plans: []planner.Plan{{Steps: []planner.Step{{Tool: "missing://"}}}}},
			tools:   tool.NewRegistry(), errContains: "open tool",
		},
		"malformed-tool-url": {
			conn:    &fakeConn{received: []chat.Message{{ConversationID: "c1"}}},
			planner: &fakePlanner{plans: []planner.Plan{{Steps: []planner.Step{{Tool: "%"}}}}},
			tools:   tool.NewRegistry(), errContains: "parse tool URL",
		},
		"invoke-tool": {
			conn:    &fakeConn{received: []chat.Message{{ConversationID: "c1"}}},
			planner: &fakePlanner{plans: []planner.Plan{{Steps: []planner.Step{{Tool: "fake://"}}}}},
			invoked: &fakeTool{err: testErr}, errContains: "invoke tool",
		},
		"close-tool": {
			conn:    &fakeConn{received: []chat.Message{{ConversationID: "c1"}}},
			planner: &fakePlanner{plans: []planner.Plan{{Steps: []planner.Step{{Tool: "fake://"}}}}},
			invoked: &fakeTool{closeErr: testErr}, errContains: "close tool",
		},
		"send-result": {
			conn:    &fakeConn{received: []chat.Message{{ConversationID: "c1"}}, sendErr: testErr},
			planner: &fakePlanner{plans: []planner.Plan{{Steps: []planner.Step{{Tool: "fake://"}}}}},
			invoked: &fakeTool{result: tool.Result{Text: "done"}}, errContains: "send result",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			if tc.invoked != nil {
				tc.tools = tool.NewRegistry(tool.Backend{Scheme: "fake", Opener: func(_ context.Context, _ *url.URL, _ cred.Store) (tool.Tool, error) {
					return tc.invoked, nil
				}})
			}
			e, err := New(Config{Chat: tc.conn, Planner: tc.planner, Tools: tc.tools})
			require.NoError(t, err)

			err = e.Run(context.Background())
			if tc.errContains != "open tool" && tc.errContains != "parse tool URL" {
				require.ErrorIs(t, err, testErr)
			}
			require.ErrorContains(t, err, tc.errContains)
			require.Equal(t, 1, tc.conn.closed)
			require.Equal(t, 1, tc.planner.closed)
			if tc.invoked != nil {
				require.Equal(t, 1, tc.invoked.closed)
			}
		})
	}
}

type panicTool struct{ closed atomic.Int32 }

func (t *panicTool) Invoke(context.Context, tool.Call) (tool.Result, error) {
	panic("boom")
}

func (t *panicTool) Close() error {
	t.closed.Add(1)
	return nil
}

func Test_Run_recovers_panicking_tool_and_closes(t *testing.T) {
	conn := &fakeConn{received: []chat.Message{{ConversationID: "c1"}}}
	p := &fakePlanner{plans: []planner.Plan{{Steps: []planner.Step{{Tool: "panic://"}}}}}
	taskTool := &panicTool{}
	tools := tool.NewRegistry(tool.Backend{Scheme: "panic", Opener: func(context.Context, *url.URL, cred.Store) (tool.Tool, error) {
		return taskTool, nil
	}})
	e, err := New(Config{Chat: conn, Planner: p, Tools: tools})
	require.NoError(t, err)
	err = e.Run(context.Background())
	require.ErrorContains(t, err, "process message: panic: boom")
	require.Equal(t, int32(1), taskTool.closed.Load())
	require.Equal(t, 1, conn.closed)
	require.Equal(t, 1, p.closed)
}

func Test_Run_context_cancellation_during_planning_is_graceful(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	conn := &fakeConn{received: []chat.Message{{ConversationID: "c1"}}}
	p := &fakePlanner{err: context.Canceled, cancel: cancel}
	e, err := New(Config{Chat: conn, Planner: p, Tools: tool.NewRegistry()})
	require.NoError(t, err)
	require.NoError(t, e.Run(ctx))
}

func Test_Run_preserves_backend_deadline(t *testing.T) {
	conn := &fakeConn{received: []chat.Message{{ConversationID: "c1"}}}
	p := &fakePlanner{err: context.DeadlineExceeded}
	e, err := New(Config{Chat: conn, Planner: p, Tools: tool.NewRegistry()})
	require.NoError(t, err)
	err = e.Run(context.Background())
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func Test_Run_preserves_errors_that_race_with_cancellation(t *testing.T) {
	testErr := errors.New("backend failed")
	testCases := map[string]struct {
		conn             *fakeConn
		planner          *fakePlanner
		cancelDuringPlan bool
	}{
		"receive": {
			conn: &fakeConn{receiveErr: testErr}, planner: &fakePlanner{},
		},
		"plan": {
			conn: &fakeConn{received: []chat.Message{{ConversationID: "c1"}}}, planner: &fakePlanner{err: testErr}, cancelDuringPlan: true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			if tc.cancelDuringPlan {
				tc.planner.cancel = cancel
			} else {
				cancel()
			}
			e, err := New(Config{Chat: tc.conn, Planner: tc.planner, Tools: tool.NewRegistry()})
			require.NoError(t, err)
			err = e.Run(ctx)
			require.ErrorIs(t, err, testErr)
		})
	}
}

func Test_Run_closed_chat_is_graceful(t *testing.T) {
	conn := &fakeConn{receiveErr: fmt.Errorf("fake: %w", chat.ErrClosed)}
	p := &fakePlanner{}
	e, err := New(Config{Chat: conn, Planner: p, Tools: tool.NewRegistry()})
	require.NoError(t, err)
	require.NoError(t, e.Run(context.Background()))
}

func Test_Run_chat_closed_while_handling_is_graceful(t *testing.T) {
	conn := &fakeConn{
		received: []chat.Message{{ConversationID: "c1"}},
		sendErr:  fmt.Errorf("fake: %w", chat.ErrClosed),
	}
	p := &fakePlanner{plans: []planner.Plan{{Steps: []planner.Step{{Tool: "fake://"}}}}}
	taskTool := &fakeTool{result: tool.Result{Text: "done"}}
	tools := tool.NewRegistry(tool.Backend{Scheme: "fake", Opener: func(context.Context, *url.URL, cred.Store) (tool.Tool, error) {
		return taskTool, nil
	}})
	e, err := New(Config{Chat: conn, Planner: p, Tools: tools})
	require.NoError(t, err)
	require.NoError(t, e.Run(context.Background()))
}

func Test_Run_accepts_canonical_reply_URL_variants(t *testing.T) {
	testCases := map[string]string{
		"canonical":  reply.URL,
		"mixed-case": "Reply://",
	}
	for name, toolURL := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			conn := &fakeConn{received: []chat.Message{{ConversationID: "c1"}}}
			p := &fakePlanner{plans: []planner.Plan{{Steps: []planner.Step{{
				Tool: toolURL,
				Call: tool.Call{Action: "send", Target: "c1", Parameters: map[string]string{"text": "hello"}},
			}}}}}
			e, err := New(Config{Chat: conn, Planner: p, Tools: tool.NewRegistry()})
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
		})
	}
}

func Test_Run_rejects_configured_reply_URL(t *testing.T) {
	testCases := map[string]string{
		"host":     "reply://send",
		"path":     "reply:///send",
		"query":    "reply://?format=text",
		"fragment": "reply://#send",
	}
	for name, toolURL := range testCases {
		t.Run(name, func(t *testing.T) {
			conn := &fakeConn{received: []chat.Message{{ConversationID: "c1"}}}
			p := &fakePlanner{plans: []planner.Plan{{Steps: []planner.Step{{
				Tool: toolURL,
				Call: tool.Call{Action: "send", Target: "c1", Parameters: map[string]string{"text": "hello"}},
			}}}}}
			e, err := New(Config{Chat: conn, Planner: p, Tools: tool.NewRegistry()})
			require.NoError(t, err)
			err = e.Run(context.Background())
			require.ErrorContains(t, err, "takes no endpoint or configuration")
			require.Empty(t, conn.sent)
		})
	}
}

func Test_Run_binds_reply_to_originating_conversation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	conn := &fakeConn{received: []chat.Message{{ConversationID: "origin"}}}
	p := &fakePlanner{plans: []planner.Plan{{Steps: []planner.Step{{
		Tool: reply.URL,
		Call: tool.Call{Action: "send", Target: "other", Parameters: map[string]string{"text": "hello"}},
	}}}}}
	e, err := New(Config{Chat: conn, Planner: p, Tools: tool.NewRegistry()})
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
	e, err := New(Config{Chat: conn, Planner: p, Tools: tool.NewRegistry(), MaxConcurrency: 2})
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
	e, err := New(Config{Chat: conn, Planner: p, Tools: tool.NewRegistry()})
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
	e, err := New(Config{Chat: conn, Planner: p, Tools: tool.NewRegistry()})
	require.NoError(t, err)
	conn.afterReceive = func() { require.NoError(t, e.Close()) }
	require.NoError(t, e.Run(context.Background()))
	require.Empty(t, p.requests)
}

func Test_processMessage_after_Close(t *testing.T) {
	conn := &fakeConn{}
	p := &fakePlanner{}
	e, err := New(Config{Chat: conn, Planner: p, Tools: tool.NewRegistry()})
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
	e, err := New(Config{Chat: conn, Planner: p, Tools: tool.NewRegistry()})
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
	e, err := New(Config{Chat: conn, Planner: p, Tools: tool.NewRegistry()})
	require.NoError(t, err)
	require.NoError(t, e.Close())

	err = e.Run(context.Background())
	require.ErrorContains(t, err, "already closed")
}

func Test_Run_rejects_concurrent_run(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	conn := &fakeConn{}
	p := &fakePlanner{}
	e, err := New(Config{Chat: conn, Planner: p, Tools: tool.NewRegistry()})
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
