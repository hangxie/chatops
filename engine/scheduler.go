package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/hangxie/chatops/chat"
)

func (e *Engine) receiveLoop(ctx context.Context, messages chan<- chat.Message, receiveErrors chan<- error) {
	for {
		msg, err := e.chat.Receive(ctx)
		if err != nil {
			receiveErrors <- err
			return
		}
		select {
		case messages <- msg:
		case <-ctx.Done():
			receiveErrors <- ctx.Err()
			return
		}
	}
}

func joinRunErrors(ctx context.Context, receiveErr, workErr error) error {
	if isGracefulStop(ctx, receiveErr) {
		receiveErr = nil
	} else if receiveErr != nil {
		receiveErr = fmt.Errorf("engine: receive message: %w", receiveErr)
	}
	if isGracefulStop(ctx, workErr) {
		workErr = nil
	}
	return errors.Join(receiveErr, workErr)
}

const maxPendingMessages = 1024

type conversationQueue struct {
	messages []chat.Message
	active   bool
	ready    bool
}

// messageScheduler runs a fixed number of workers while allowing at most one
// worker into a conversation queue at a time.
type messageScheduler struct {
	ctx    context.Context
	handle func(chat.Message) error

	mu            sync.Mutex
	cond          *sync.Cond
	conversations map[string]*conversationQueue
	ready         []string
	pending       int
	stopped       bool
	err           error
	done          chan struct{}
	doneOnce      sync.Once
	workers       sync.WaitGroup
	stopContext   func() bool
}

func newMessageScheduler(ctx context.Context, concurrency int, handle func(chat.Message) error) *messageScheduler {
	s := &messageScheduler{
		ctx:           ctx,
		handle:        handle,
		conversations: make(map[string]*conversationQueue),
		done:          make(chan struct{}),
	}
	s.cond = sync.NewCond(&s.mu)
	s.stopContext = context.AfterFunc(ctx, s.Stop)
	s.workers.Add(concurrency)
	for range concurrency {
		go s.worker()
	}
	return s
}

func (s *messageScheduler) Submit(msg chat.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.pending >= maxPendingMessages && !s.stopped {
		s.cond.Wait()
	}
	if s.stopped {
		if s.err != nil {
			return s.err
		}
		if err := s.ctx.Err(); err != nil {
			return err
		}
		return context.Canceled
	}
	queue := s.conversations[msg.ConversationID]
	if queue == nil {
		queue = &conversationQueue{}
		s.conversations[msg.ConversationID] = queue
	}
	queue.messages = append(queue.messages, msg)
	s.pending++
	if !queue.active && !queue.ready {
		s.markReady(msg.ConversationID, queue)
	}
	return nil
}

func (s *messageScheduler) Done() <-chan struct{} {
	return s.done
}

func (s *messageScheduler) Stop() {
	s.stop(nil)
}

func (s *messageScheduler) Wait() error {
	s.workers.Wait()
	s.stopContext()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *messageScheduler) worker() {
	defer s.workers.Done()
	for {
		conversationID, msg, ok := s.next()
		if !ok {
			return
		}
		err := s.handle(msg)
		s.complete(conversationID, err)
	}
}

func (s *messageScheduler) next() (string, chat.Message, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.ready) == 0 && !s.stopped {
		s.cond.Wait()
	}
	if s.stopped {
		return "", chat.Message{}, false
	}
	conversationID := s.ready[0]
	s.ready = s.ready[1:]
	queue := s.conversations[conversationID]
	queue.ready = false
	queue.active = true
	msg := queue.messages[0]
	queue.messages = queue.messages[1:]
	s.pending--
	s.cond.Broadcast()
	return conversationID, msg, true
}

func (s *messageScheduler) complete(conversationID string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	queue := s.conversations[conversationID]
	queue.active = false
	if err != nil {
		if s.err == nil {
			s.err = err
		}
		s.stopLocked()
		return
	}
	if s.stopped {
		return
	}
	if len(queue.messages) == 0 {
		delete(s.conversations, conversationID)
		return
	}
	s.markReady(conversationID, queue)
}

func (s *messageScheduler) markReady(conversationID string, queue *conversationQueue) {
	queue.ready = true
	s.ready = append(s.ready, conversationID)
	s.cond.Signal()
}

func (s *messageScheduler) stop(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil && s.err == nil {
		s.err = err
	}
	s.stopLocked()
}

func (s *messageScheduler) stopLocked() {
	if s.stopped {
		return
	}
	s.stopped = true
	s.doneOnce.Do(func() { close(s.done) })
	s.cond.Broadcast()
}
