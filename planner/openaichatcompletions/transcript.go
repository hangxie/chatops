package openaichatcompletions

import (
	"sync"
	"time"
)

const (
	// defaultTranscriptTTL bounds how long an in-flight turn's transcript
	// survives without progress, a backstop against a missed EndTurn.
	defaultTranscriptTTL = 10 * time.Minute
	// defaultMaxTranscripts caps the number of concurrent in-flight turns
	// remembered at once.
	defaultMaxTranscripts = 1024
)

// turnTranscript is the provider message history of one in-flight turn plus its
// expiry for the bounded store.
type turnTranscript struct {
	messages []reqMessage
	expiry   time.Time
}

// transcriptStore holds the in-flight turn transcripts keyed by
// (connectionID, conversationID). It is bounded by a TTL and a capacity, the
// same discipline the ping planner uses for pending confirmations, so an
// abandoned turn cannot leak state. It is safe for concurrent use.
type transcriptStore struct {
	mu       sync.Mutex
	ttl      time.Duration
	capacity int
	now      func() time.Time
	entries  map[string]*turnTranscript
}

func newTranscriptStore(ttl time.Duration, capacity int) *transcriptStore {
	return &transcriptStore{
		ttl:      ttl,
		capacity: capacity,
		now:      time.Now,
		entries:  make(map[string]*turnTranscript),
	}
}

// transcriptKey combines the two ids that scope a turn; conversation ids are
// only unique within one connection.
func transcriptKey(connectionID, conversationID string) string {
	return connectionID + "\x00" + conversationID
}

// load returns the transcript messages for a turn, or (nil, false) when none is
// stored or it has expired.
func (s *transcriptStore) load(connectionID, conversationID string) ([]reqMessage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := transcriptKey(connectionID, conversationID)
	entry, ok := s.entries[key]
	if !ok {
		return nil, false
	}
	if !s.now().Before(entry.expiry) {
		delete(s.entries, key)
		return nil, false
	}
	return entry.messages, true
}

// save stores messages for a turn with a fresh expiry, evicting expired and, if
// still over capacity, the earliest-expiring entry.
func (s *transcriptStore) save(connectionID, conversationID string, messages []reqMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := transcriptKey(connectionID, conversationID)
	s.entries[key] = &turnTranscript{messages: messages, expiry: s.now().Add(s.ttl)}
	if len(s.entries) <= s.capacity {
		return
	}
	s.evictLocked(key)
}

// drop removes a turn's transcript. It is the EndTurn cleanup path.
func (s *transcriptStore) drop(connectionID, conversationID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, transcriptKey(connectionID, conversationID))
}

// evictLocked removes expired entries and, if still over capacity, the entry
// with the earliest expiry. keep is never evicted (it was just saved).
func (s *transcriptStore) evictLocked(keep string) {
	now := s.now()
	for k, e := range s.entries {
		if k != keep && !now.Before(e.expiry) {
			delete(s.entries, k)
		}
	}
	for len(s.entries) > s.capacity {
		var oldestKey string
		var oldest time.Time
		for k, e := range s.entries {
			if k == keep {
				continue
			}
			if oldestKey == "" || e.expiry.Before(oldest) {
				oldestKey, oldest = k, e.expiry
			}
		}
		if oldestKey == "" {
			return
		}
		delete(s.entries, oldestKey)
	}
}
