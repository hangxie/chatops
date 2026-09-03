package openaichatcompletions

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func Test_transcriptStore_saveLoadDrop(t *testing.T) {
	s := newTranscriptStore(time.Minute, 4)
	msgs := []reqMessage{{Role: "user", Content: "hi"}}

	_, ok := s.load("c", "conv")
	require.False(t, ok)

	s.save("c", "conv", msgs)
	got, ok := s.load("c", "conv")
	require.True(t, ok)
	require.Equal(t, msgs, got)

	// A different connection with the same conversation id is a separate turn.
	_, ok = s.load("other", "conv")
	require.False(t, ok)

	s.drop("c", "conv")
	_, ok = s.load("c", "conv")
	require.False(t, ok)
}

func Test_transcriptStore_expiry(t *testing.T) {
	now := time.Unix(0, 0)
	s := newTranscriptStore(time.Minute, 4)
	s.now = func() time.Time { return now }

	s.save("c", "conv", []reqMessage{{Role: "user"}})
	now = now.Add(2 * time.Minute)

	_, ok := s.load("c", "conv")
	require.False(t, ok, "an expired transcript is not returned")
}

func Test_transcriptStore_capacityEvictsEarliestExpiry(t *testing.T) {
	now := time.Unix(0, 0)
	s := newTranscriptStore(time.Hour, 2)
	s.now = func() time.Time { return now }

	s.save("c", "a", []reqMessage{{Content: "a"}})
	now = now.Add(time.Second)
	s.save("c", "b", []reqMessage{{Content: "b"}})
	now = now.Add(time.Second)
	s.save("c", "c", []reqMessage{{Content: "c"}}) // over capacity → evict "a"

	_, ok := s.load("c", "a")
	require.False(t, ok, "earliest-expiring entry is evicted at capacity")
	_, okB := s.load("c", "b")
	_, okC := s.load("c", "c")
	require.True(t, okB)
	require.True(t, okC)
}
