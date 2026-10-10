// Package conversation runs agent turns one at a time per conversation, many conversations at once.
package conversation

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/hangxie/chatops/internal/agent"
	"github.com/hangxie/chatops/internal/config"
	"github.com/hangxie/chatops/internal/llm"
)

// Submit errors.
var (
	ErrBusy   = errors.New("too many pending messages")
	ErrClosed = errors.New("conversation manager is shut down")
)

const (
	sweepInterval = time.Minute
	replyTimeout  = 30 * time.Second
)

// Message is one inbound chat message whose sender the chat adapter has already verified.
type Message struct {
	// Conversation keys history and ordering; one chat thread maps to one key.
	Conversation string
	User         string
	Text         string
	// Reply posts text back into the message's thread.
	Reply func(ctx context.Context, text string) error
}

// Runner runs one turn; *agent.Agent satisfies it.
type Runner interface {
	Run(ctx context.Context, history []llm.Message, input string) (agent.Turn, error)
}

// Manager queues messages per conversation and keeps each conversation's recent history.
type Manager struct {
	ctx    context.Context
	runner Runner
	limits config.Agent
	logger *slog.Logger
	now    func() time.Time
	slots  chan struct{}
	turns  sync.WaitGroup

	mu        sync.Mutex
	pending   int
	threads   map[string]*thread
	lastSweep time.Time
}

// thread is one conversation's queue and history; Manager.mu guards it.
type thread struct {
	queue   []Message
	running bool
	turns   [][]llm.Message
	// updated is the last finished attempt, failed or not; history expires history_ttl after it.
	updated time.Time
}

// New builds a manager whose turns run under ctx; cancel ctx, then call Wait, to shut down.
func New(ctx context.Context, runner Runner, limits config.Agent, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		ctx:     ctx,
		runner:  runner,
		limits:  limits,
		logger:  logger,
		now:     time.Now,
		slots:   make(chan struct{}, limits.MaxConcurrentTurns),
		threads: map[string]*thread{},
	}
}

// Submit queues msg behind earlier messages of the same conversation.
func (m *Manager) Submit(msg Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx.Err() != nil {
		return ErrClosed
	}
	if m.pending >= m.limits.MaxPendingMessages {
		return ErrBusy
	}
	now := m.now()
	m.sweep(now)
	t, ok := m.threads[msg.Conversation]
	if !ok {
		t = &thread{updated: now}
		m.threads[msg.Conversation] = t
	}
	t.queue = append(t.queue, msg)
	m.pending++
	if !t.running {
		t.running = true
		m.turns.Add(1)
		go m.drain(t)
	}
	return nil
}

// Wait blocks until every turn has finished; call it after cancelling the manager's context.
func (m *Manager) Wait() {
	m.turns.Wait()
}

// drain answers t's queued messages in order, then exits until the next Submit.
func (m *Manager) drain(t *thread) {
	defer m.turns.Done()
	for {
		m.mu.Lock()
		if len(t.queue) == 0 || m.ctx.Err() != nil {
			m.pending -= len(t.queue)
			t.queue, t.running = nil, false
			m.mu.Unlock()
			return
		}
		msg := t.queue[0]
		t.queue = t.queue[1:]
		var history []llm.Message
		if m.limits.HistoryTurns > 0 {
			history = t.history(m.now(), m.limits.HistoryTTL)
		}
		m.mu.Unlock()

		m.answer(t, msg, history)

		m.mu.Lock()
		m.pending--
		m.mu.Unlock()
	}
}

// answer runs one turn, then replies; drain waits for the reply, keeping the thread in order.
func (m *Manager) answer(t *thread, msg Message, history []llm.Message) {
	log := m.logger.With("conversation", msg.Conversation, "user", msg.User)
	turn, err := m.run(history, msg.Text)
	if m.ctx.Err() != nil {
		return
	}
	m.mu.Lock()
	t.updated = m.now()
	if err == nil && m.limits.HistoryTurns > 0 {
		t.record(turn.Messages, m.limits.HistoryTurns)
	}
	m.mu.Unlock()
	reply := turn.Reply
	if err != nil {
		log.Error("turn failed", "error", err)
		reply = failureReply(err)
	}

	ctx, cancel := context.WithTimeout(m.ctx, replyTimeout)
	defer cancel()
	if err := msg.Reply(ctx, reply); err != nil {
		log.Warn("reply failed", "error", err)
	}
}

// run holds a turn slot only while the agent works, not while the reply is delivered.
func (m *Manager) run(history []llm.Message, input string) (agent.Turn, error) {
	select {
	case m.slots <- struct{}{}:
	case <-m.ctx.Done():
		return agent.Turn{}, m.ctx.Err()
	}
	defer func() { <-m.slots }()
	return m.runner.Run(m.ctx, history, input)
}

// failureReply names the failure kind only; details may hold internal URLs, so they go to the log.
func failureReply(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "Sorry, I ran out of time on that request. Try a narrower question."
	}
	return "Sorry, I couldn't complete that request; the error has been logged."
}

// sweep drops idle conversations whose history has expired, at most once per sweepInterval.
func (m *Manager) sweep(now time.Time) {
	if now.Sub(m.lastSweep) < sweepInterval {
		return
	}
	m.lastSweep = now
	for key, t := range m.threads {
		if !t.running && now.Sub(t.updated) >= m.limits.HistoryTTL {
			delete(m.threads, key)
		}
	}
}

func (t *thread) history(now time.Time, ttl time.Duration) []llm.Message {
	if now.Sub(t.updated) >= ttl {
		t.turns = nil
	}
	var history []llm.Message
	for _, turn := range t.turns {
		history = append(history, turn...)
	}
	return history
}

func (t *thread) record(messages []llm.Message, maxTurns int) {
	t.turns = append(t.turns, messages)
	if len(t.turns) > maxTurns {
		t.turns = t.turns[len(t.turns)-maxTurns:]
	}
}
