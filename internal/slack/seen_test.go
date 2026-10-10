package slack

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func Test_seenSet(t *testing.T) {
	start := time.Unix(1000, 0)

	t.Run("duplicate-within-ttl", func(t *testing.T) {
		s := newSeenSet(time.Minute, 10)
		require.False(t, s.has("a", start))
		s.add("a", start)
		require.True(t, s.has("a", start.Add(59*time.Second)))
		require.False(t, s.has("b", start))
	})

	t.Run("forgotten-after-ttl", func(t *testing.T) {
		s := newSeenSet(time.Minute, 10)
		s.add("a", start)
		require.False(t, s.has("a", start.Add(time.Minute)))
	})

	t.Run("oldest-evicted-at-capacity", func(t *testing.T) {
		s := newSeenSet(time.Hour, 3)
		for i := range 4 {
			s.add(fmt.Sprint(i), start)
		}
		require.Len(t, s.at, 3)
		require.False(t, s.has("0", start), "evicted key counts as new")
		require.True(t, s.has("3", start))
	})

	t.Run("add-twice-keeps-one", func(t *testing.T) {
		s := newSeenSet(time.Minute, 10)
		s.add("a", start)
		s.add("a", start.Add(time.Second))
		require.Equal(t, []string{"a"}, s.order)
		require.Equal(t, start, s.at["a"])
	})

	t.Run("re-added-after-expiry", func(t *testing.T) {
		s := newSeenSet(time.Minute, 10)
		s.add("a", start)
		later := start.Add(time.Minute)
		require.False(t, s.has("a", later))
		s.add("a", later)
		require.True(t, s.has("a", later.Add(59*time.Second)))
	})
}
