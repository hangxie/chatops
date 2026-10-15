package conversation

import (
	"time"

	"github.com/hangxie/chatops/internal/agent"
)

// Reasons a question is rejected before it runs.
const (
	RejectBusy   = "busy"
	RejectClosed = "closed"
)

// Observer receives question events, such as for metrics; calls run on the manager's goroutines and must not block.
type Observer interface {
	// Received counts every inbound question, including ones then rejected.
	Received(backend string)
	// Rejected reports a question refused before running, for RejectBusy or RejectClosed.
	Rejected(backend, reason string)
	// Started reports how long a question waited, behind its conversation and for a turn slot.
	Started(backend string, queued time.Duration)
	// Finished reports a turn's stats, whether or not its reply is then delivered.
	Finished(backend string, stats agent.Stats)
	// Delivered reports a posted reply and the time since its question was received.
	Delivered(backend string, outcome agent.Outcome, elapsed time.Duration)
	// ReplyFailed reports a reply that could not be posted.
	ReplyFailed(backend string)
}

type nopObserver struct{}

func (nopObserver) Received(string)                                {}
func (nopObserver) Rejected(string, string)                        {}
func (nopObserver) Started(string, time.Duration)                  {}
func (nopObserver) Finished(string, agent.Stats)                   {}
func (nopObserver) Delivered(string, agent.Outcome, time.Duration) {}
func (nopObserver) ReplyFailed(string)                             {}
