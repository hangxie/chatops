package slack

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func Test_routeCache_evicts_inactive_conversations(t *testing.T) {
	now := time.Unix(1000, 0)
	cache := newRouteCache(time.Hour, 10)
	cache.now = func() time.Time { return now }
	first := conversation{channel: "C1", thread: "1.1"}
	second := conversation{channel: "C2", thread: "2.2"}

	cache.Remember("first", first)
	now = now.Add(30 * time.Minute)
	route, ok := cache.Lookup("first")
	require.True(t, ok)
	require.Equal(t, first, route)

	// Lookup refreshes activity, so the first route survives one hour from that use.
	now = now.Add(59 * time.Minute)
	cache.Remember("second", second)
	_, ok = cache.Lookup("first")
	require.True(t, ok)

	now = now.Add(time.Hour)
	_, ok = cache.Lookup("first")
	require.False(t, ok)
	require.Empty(t, cache.routes)
}

func Test_routeCache_replaces_route(t *testing.T) {
	cache := newRouteCache(time.Hour, 10)
	cache.Remember("id", conversation{channel: "C1", thread: "1.1"})
	cache.Remember("id", conversation{channel: "C2", thread: "2.2"})
	route, ok := cache.Lookup("id")
	require.True(t, ok)
	require.Equal(t, conversation{channel: "C2", thread: "2.2"}, route)
	require.Len(t, cache.routes, 1)
}

func Test_routeCache_enforces_capacity(t *testing.T) {
	now := time.Unix(1000, 0)
	cache := newRouteCache(time.Hour, 2)
	cache.now = func() time.Time { return now }
	cache.Remember("first", conversation{channel: "C1", thread: "1.1"})

	now = now.Add(time.Minute)
	cache.Remember("second", conversation{channel: "C2", thread: "2.2"})
	now = now.Add(time.Minute)
	_, ok := cache.Lookup("first") // Refresh first, making second the oldest route.
	require.True(t, ok)

	now = now.Add(time.Minute)
	cache.Remember("third", conversation{channel: "C3", thread: "3.3"})
	require.Len(t, cache.routes, 2)
	_, ok = cache.Lookup("second")
	require.False(t, ok)
	_, ok = cache.Lookup("first")
	require.True(t, ok)
	_, ok = cache.Lookup("third")
	require.True(t, ok)
}
