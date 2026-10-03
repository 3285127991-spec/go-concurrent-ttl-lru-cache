// Package ttlcache implements a concurrency-safe in-memory cache with
// a maximum entry limit, LRU eviction and per-entry TTL.
package ttlcache

import (
	"container/list"
	"sync"
	"time"
)

// Stats is a point-in-time snapshot of the cache counters.
type Stats struct {
	Hits    uint64 // Get calls that returned a live entry
	Misses  uint64 // Get calls that returned nothing (including expired entries)
	Expired uint64 // entries removed because their TTL elapsed (via Get, eviction scan or PurgeExpired)
	Evicted uint64 // live entries evicted to make room for a new entry
	Len     int    // entries currently stored (including not-yet-purged expired ones)
}

// entry is the value stored in the LRU list elements.
type entry struct {
	key       string
	value     []byte
	expiresAt time.Time // zero value means "never expires"
}

// expired reports whether the entry is expired at the given time.
func (e *entry) expired(now time.Time) bool {
	return !e.expiresAt.IsZero() && !now.Before(e.expiresAt)
}

// Cache is a goroutine-safe in-memory cache. The zero value is not usable;
// create one with New.
type Cache struct {
	mu       sync.Mutex
	capacity int
	items    map[string]*list.Element
	lru      *list.List // front = most recently used

	hits    uint64
	misses  uint64
	expired uint64
	evicted uint64

	// now returns the current time; replaceable in tests.
	now func() time.Time
}

// New creates a Cache that holds at most capacity entries.
// It panics if capacity < 1.
func New(capacity int) *Cache {
	if capacity < 1 {
		panic("ttlcache: capacity must be >= 1")
	}
	return &Cache{
		capacity: capacity,
		items:    make(map[string]*list.Element),
		lru:      list.New(),
		now:      time.Now,
	}
}

// Set stores value under key. A ttl <= 0 means the entry never expires.
// Setting an existing key replaces its value and TTL and marks it as most
// recently used, without creating a duplicate entry.
func (c *Cache) Set(key string, value []byte, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[key]; ok {
		ent := el.Value.(*entry)
		ent.value = cloneBytes(value)
		ent.expiresAt = deadline(c.now(), ttl)
		c.lru.MoveToFront(el)
		return
	}

	for len(c.items) >= c.capacity {
		if !c.evictOneLocked() {
			break // defensive: list and map disagree; avoid an infinite loop
		}
	}

	el := c.lru.PushFront(&entry{
		key:       key,
		value:     cloneBytes(value),
		expiresAt: deadline(c.now(), ttl),
	})
	c.items[key] = el
}

// Get returns the value stored under key. The second return value reports
// whether a live entry was found. Expired entries are treated as absent and
// are removed on access. The returned slice is a copy.
func (c *Cache) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.items[key]
	if !ok {
		c.misses++
		return nil, false
	}
	ent := el.Value.(*entry)
	if ent.expired(c.now()) {
		c.removeLocked(el)
		c.expired++
		c.misses++
		return nil, false
	}
	c.lru.MoveToFront(el)
	c.hits++
	return cloneBytes(ent.value), true
}

// Delete removes key from the cache. It reports whether the key was present.
func (c *Cache) Delete(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.items[key]
	if !ok {
		return false
	}
	c.removeLocked(el)
	return true
}

// Clear removes all entries. Cumulative counters (Hits, Misses, Expired,
// Evicted) are NOT reset; only Len returns to zero.
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items = make(map[string]*list.Element)
	c.lru.Init()
}

// PurgeExpired removes every expired entry and returns how many were removed.
func (c *Cache) PurgeExpired() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	removed := 0
	for el := c.lru.Back(); el != nil; {
		prev := el.Prev()
		if el.Value.(*entry).expired(now) {
			c.removeLocked(el)
			c.expired++
			removed++
		}
		el = prev
	}
	return removed
}

// Len returns the number of entries currently stored, including expired
// entries that have not been purged yet.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// Stats returns a snapshot of the cache counters.
func (c *Cache) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Stats{
		Hits:    c.hits,
		Misses:  c.misses,
		Expired: c.expired,
		Evicted: c.evicted,
		Len:     len(c.items),
	}
}

// evictOneLocked makes room for one new entry. It scans from the
// least-recently-used end, dropping expired entries (counted as expired)
// and evicting the first live entry found (counted as evicted). It reports
// whether a live entry was evicted.
func (c *Cache) evictOneLocked() bool {
	now := c.now()
	for el := c.lru.Back(); el != nil; el = el.Prev() {
		ent := el.Value.(*entry)
		if ent.expired(now) {
			c.removeLocked(el)
			c.expired++
			continue
		}
		c.removeLocked(el)
		c.evicted++
		return true
	}
	return false
}

// removeLocked unlinks el from the LRU list and the index map.
func (c *Cache) removeLocked(el *list.Element) {
	c.lru.Remove(el)
	delete(c.items, el.Value.(*entry).key)
}

func deadline(now time.Time, ttl time.Duration) time.Time {
	if ttl <= 0 {
		return time.Time{}
	}
	return now.Add(ttl)
}

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
