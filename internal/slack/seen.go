package slack

import "time"

// seenSet remembers recent message IDs so Slack retries are answered once.
type seenSet struct {
	ttl   time.Duration
	limit int
	at    map[string]time.Time
	order []string
}

func newSeenSet(ttl time.Duration, limit int) *seenSet {
	return &seenSet{ttl: ttl, limit: limit, at: map[string]time.Time{}}
}

// has reports whether id was added within the TTL.
func (s *seenSet) has(id string, now time.Time) bool {
	s.expire(now)
	_, ok := s.at[id]
	return ok
}

// add records id; callers add only admitted messages, so a refused one can be redelivered.
func (s *seenSet) add(id string, now time.Time) {
	s.expire(now)
	if _, ok := s.at[id]; ok {
		return
	}
	for len(s.order) >= s.limit {
		s.drop()
	}
	s.at[id] = now
	s.order = append(s.order, id)
}

// expire drops IDs older than the TTL; IDs are added in time order, so the oldest is first.
func (s *seenSet) expire(now time.Time) {
	for len(s.order) > 0 && now.Sub(s.at[s.order[0]]) >= s.ttl {
		s.drop()
	}
}

func (s *seenSet) drop() {
	delete(s.at, s.order[0])
	s.order = s.order[1:]
}
