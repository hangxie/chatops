// Package engine connects a chat bot, planner, and operational tools into a
// long-running message-processing service.
//
// An Engine receives chat messages, preserves ordering within each
// conversation, and processes independent conversations through a bounded
// worker pool. For each message it asks the planner for an ordered plan,
// invokes each tool, and posts every non-empty tool result back to the
// conversation that produced the plan. Planner replies, including
// clarification and confirmation prompts, are ordinary reply:// tool steps.
package engine

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/hangxie/chatops/chat"
	"github.com/hangxie/chatops/cred"
	"github.com/hangxie/chatops/planner"
	"github.com/hangxie/chatops/tool"
	"github.com/hangxie/chatops/tool/reply"
)

// Config contains the opened components used by an Engine. The engine owns
// Chat and Planner after New succeeds and closes both when Run returns or
// Close is called. Tools are opened from Tools for individual plan steps;
// Credentials is passed to their openers and remains owned by the caller.
type Config struct {
	// ConnectionID scopes planner conversation state to this chat connection.
	ConnectionID string

	Chat        chat.Conn
	Planner     planner.Planner
	Tools       *tool.Registry
	Credentials cred.Store

	// MaxConcurrency is the maximum number of conversations processed at once.
	// Zero uses DefaultMaxConcurrency.
	MaxConcurrency int
}

const (
	// DefaultMaxConcurrency is the default number of conversations an Engine
	// processes concurrently.
	DefaultMaxConcurrency = 8

	// MaximumMaxConcurrency bounds the number of Engine worker goroutines.
	MaximumMaxConcurrency = 256
)

// Engine is a single-chat-connection message processing service.
type Engine struct {
	connectionID   string
	chat           chat.Conn
	planner        planner.Planner
	tools          *tool.Registry
	credentials    cred.Store
	reply          tool.Tool
	maxConcurrency int

	mu       sync.Mutex
	work     sync.WaitGroup
	run      bool
	closed   bool
	cancel   context.CancelFunc
	closeErr error
}

// New validates config and constructs an Engine. It does not start receiving
// messages; call Run to serve until the context is cancelled or an error
// occurs.
func New(config Config) (*Engine, error) {
	if config.Chat == nil {
		return nil, errors.New("engine: nil chat connection")
	}
	if config.Planner == nil {
		return nil, errors.New("engine: nil planner")
	}
	if config.Tools == nil {
		return nil, errors.New("engine: nil tool registry")
	}
	if config.MaxConcurrency < 0 {
		return nil, errors.New("engine: negative maximum concurrency")
	}
	if config.MaxConcurrency > MaximumMaxConcurrency {
		return nil, fmt.Errorf("engine: maximum concurrency exceeds %d", MaximumMaxConcurrency)
	}
	maxConcurrency := config.MaxConcurrency
	if maxConcurrency == 0 {
		maxConcurrency = DefaultMaxConcurrency
	}
	// Open can only reject a nil connection, which was checked above.
	replyTool, _ := reply.Open(context.Background(), config.Chat)
	return &Engine{
		connectionID:   config.ConnectionID,
		chat:           config.Chat,
		planner:        config.Planner,
		tools:          config.Tools,
		credentials:    config.Credentials,
		reply:          replyTool,
		maxConcurrency: maxConcurrency,
	}, nil
}

// Run serves messages until ctx is cancelled, the chat connection reports
// chat.ErrClosed, or processing fails. Cancellation and chat.ErrClosed are
// graceful stops and return nil; other errors are returned even if they race
// with cancellation. Run always closes the engine before it returns and may
// only be called once.
func (e *Engine) Run(ctx context.Context) (err error) {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return errors.New("engine: already closed")
	}
	if e.run {
		e.mu.Unlock()
		return errors.New("engine: already running")
	}
	e.run = true
	runCtx, cancel := context.WithCancel(ctx)
	e.cancel = cancel
	e.mu.Unlock()

	defer func() {
		cancel()
		err = errors.Join(err, e.Close())
	}()
	scheduler := newMessageScheduler(runCtx, e.maxConcurrency, func(msg chat.Message) error {
		return e.processMessage(runCtx, msg)
	})
	defer scheduler.Stop()
	messages := make(chan chat.Message, 1)
	receiveErrors := make(chan error, 1)
	go e.receiveLoop(runCtx, messages, receiveErrors)

	for {
		select {
		case msg := <-messages:
			if submitErr := scheduler.Submit(msg); submitErr != nil {
				return submitErr
			}
		case receiveErr := <-receiveErrors:
			cancel()
			scheduler.Stop()
			return joinRunErrors(runCtx, receiveErr, scheduler.Wait())
		case <-scheduler.Done():
			cancel()
			receiveErr := <-receiveErrors
			return joinRunErrors(runCtx, receiveErr, scheduler.Wait())
		case <-runCtx.Done():
			scheduler.Stop()
			receiveErr := <-receiveErrors
			return joinRunErrors(runCtx, receiveErr, scheduler.Wait())
		}
	}
}

// beginWork excludes Close from planner and tool work.
func (e *Engine) beginWork() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return false
	}
	e.work.Add(1)
	return true
}

func (e *Engine) processMessage(ctx context.Context, msg chat.Message) (err error) {
	if !e.beginWork() {
		return context.Canceled
	}
	defer e.work.Done()
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("engine: process message: panic: %v", recovered)
		}
	}()
	return e.handle(ctx, msg)
}

func isGracefulStop(ctx context.Context, err error) bool {
	return errors.Is(err, chat.ErrClosed) ||
		(errors.Is(ctx.Err(), context.Canceled) && errors.Is(err, context.Canceled)) ||
		(errors.Is(ctx.Err(), context.DeadlineExceeded) && errors.Is(err, context.DeadlineExceeded))
}

func (e *Engine) handle(ctx context.Context, msg chat.Message) error {
	plan, err := e.planner.Plan(ctx, planner.Request{
		Text:           msg.Text,
		ConnectionID:   e.connectionID,
		ConversationID: msg.ConversationID,
		Sender:         msg.Sender,
	})
	if err != nil {
		return fmt.Errorf("engine: plan message: %w", err)
	}
	for i, step := range plan.Steps {
		result, invokeErr := e.invoke(ctx, msg.ConversationID, step)
		if invokeErr != nil {
			return fmt.Errorf("engine: execute step %d (%q): %w", i+1, step.Tool, invokeErr)
		}
		if result.Text == "" {
			continue
		}
		if sendErr := e.chat.Send(ctx, chat.Message{ConversationID: msg.ConversationID, Text: result.Text}); sendErr != nil {
			return fmt.Errorf("engine: send result for step %d (%q): %w", i+1, step.Tool, sendErr)
		}
	}
	return nil
}

func (e *Engine) invoke(ctx context.Context, conversationID string, step planner.Step) (result tool.Result, err error) {
	u, err := url.Parse(step.Tool)
	if err != nil {
		return tool.Result{}, fmt.Errorf("parse tool URL: %w", err)
	}
	if strings.EqualFold(u.Scheme, reply.Scheme) {
		if !strings.EqualFold(step.Tool, reply.URL) {
			return tool.Result{}, fmt.Errorf("reply: URL %q takes no endpoint or configuration", step.Tool)
		}
		step.Call.Target = conversationID
		return e.reply.Invoke(ctx, step.Call)
	}

	// Tool instances are deliberately scoped to one step for isolated ownership
	// and cleanup. Expensive backends can pool resources behind their opener.
	opened, err := e.tools.Open(ctx, step.Tool, e.credentials)
	if err != nil {
		return tool.Result{}, fmt.Errorf("open tool: %w", err)
	}
	defer func() {
		if closeErr := opened.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close tool: %w", closeErr))
		}
	}()
	result, err = opened.Invoke(ctx, step.Call)
	if err != nil {
		return tool.Result{}, fmt.Errorf("invoke tool: %w", err)
	}
	return result, nil
}

// Close cancels Run, waits for in-flight planning and tool work, then releases
// the planner and chat connection. It is idempotent and joins errors from all
// components so one failed cleanup does not skip another.
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return e.closeErr
	}
	e.closed = true
	if e.cancel != nil {
		e.cancel()
	}
	e.work.Wait()
	e.closeErr = errors.Join(
		closeComponent("planner", e.planner.Close()),
		closeComponent("chat", e.chat.Close()),
	)
	return e.closeErr
}

func closeComponent(name string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("engine: close %s: %w", name, err)
}
