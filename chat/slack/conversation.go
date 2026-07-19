package slack

import (
	"container/heap"
	"sync"
	"time"
)

const (
	defaultConversationTTL       = 24 * time.Hour
	defaultMaxConversationRoutes = 4096
)

type conversation struct {
	channel string
	thread  string
}

type route struct {
	id           string
	conversation conversation
	expiresAt    time.Time
	heapIndex    int
}

type routeExpiryHeap []*route

func (h routeExpiryHeap) Len() int           { return len(h) }
func (h routeExpiryHeap) Less(i, j int) bool { return h[i].expiresAt.Before(h[j].expiresAt) }
func (h routeExpiryHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].heapIndex = i
	h[j].heapIndex = j
}

func (h *routeExpiryHeap) Push(value any) {
	route := value.(*route)
	route.heapIndex = len(*h)
	*h = append(*h, route)
}

func (h *routeExpiryHeap) Pop() any {
	old := *h
	last := len(old) - 1
	route := old[last]
	old[last] = nil
	route.heapIndex = -1
	*h = old[:last]
	return route
}

// routeCache retains a bounded set of Slack-native routes. Its indexed expiry
// heap makes refresh and eviction O(log n), without scanning the route map.
type routeCache struct {
	mu        sync.Mutex
	routes    map[string]*route
	expiry    routeExpiryHeap
	ttl       time.Duration
	maxRoutes int
	now       func() time.Time
}

func newRouteCache(ttl time.Duration, maxRoutes int) *routeCache {
	return &routeCache{
		routes:    make(map[string]*route, maxRoutes),
		expiry:    make(routeExpiryHeap, 0, maxRoutes),
		ttl:       ttl,
		maxRoutes: maxRoutes,
		now:       time.Now,
	}
}

func (c *routeCache) Remember(id string, conversation conversation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.evictExpired(now)
	if existing, ok := c.routes[id]; ok {
		existing.conversation = conversation
		c.refresh(existing, now)
		return
	}
	if len(c.routes) >= c.maxRoutes {
		c.evictEarliestExpiry()
	}
	route := &route{id: id, conversation: conversation, expiresAt: now.Add(c.ttl)}
	c.routes[id] = route
	heap.Push(&c.expiry, route)
}

func (c *routeCache) Lookup(id string) (conversation, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.evictExpired(now)
	route, ok := c.routes[id]
	if !ok {
		return conversation{}, false
	}
	c.refresh(route, now)
	return route.conversation, true
}

func (c *routeCache) refresh(route *route, now time.Time) {
	route.expiresAt = now.Add(c.ttl)
	heap.Fix(&c.expiry, route.heapIndex)
}

func (c *routeCache) evictExpired(now time.Time) {
	for c.expiry.Len() > 0 && !c.expiry[0].expiresAt.After(now) {
		c.evictEarliestExpiry()
	}
}

func (c *routeCache) evictEarliestExpiry() {
	earliest := heap.Pop(&c.expiry).(*route)
	delete(c.routes, earliest.id)
}
