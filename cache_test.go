package ttlcache

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeClock is a manually advanced clock for deterministic TTL tests.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

func newTestCache(capacity int) (*Cache, *fakeClock) {
	clock := newFakeClock()
	c := New(capacity)
	c.now = clock.Now
	return c, clock
}

func mustGet(t *testing.T, c *Cache, key, want string) {
	t.Helper()
	got, ok := c.Get(key)
	if !ok {
		t.Fatalf("Get(%q): expected hit, got miss", key)
	}
	if string(got) != want {
		t.Fatalf("Get(%q) = %q, want %q", key, got, want)
	}
}

func mustMiss(t *testing.T, c *Cache, key string) {
	t.Helper()
	if v, ok := c.Get(key); ok {
		t.Fatalf("Get(%q): expected miss, got %q", key, v)
	}
}

func TestSetGet(t *testing.T) {
	c, _ := newTestCache(4)

	c.Set("a", []byte("alpha"), 0)
	c.Set("b", []byte{0x00, 0x01, 0xff}, 0)

	mustGet(t, c, "a", "alpha")
	got, ok := c.Get("b")
	if !ok || len(got) != 3 || got[2] != 0xff {
		t.Fatalf("Get(b) = %v, %v; want binary bytes", got, ok)
	}
	mustMiss(t, c, "missing")

	if c.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", c.Len())
	}
}

func TestSetOverwrite(t *testing.T) {
	c, _ := newTestCache(2)

	c.Set("a", []byte("old"), 0)
	c.Set("b", []byte("bee"), 0)
	c.Set("a", []byte("new"), 0) // overwrite: no duplicate, "a" becomes MRU

	if c.Len() != 2 {
		t.Fatalf("Len() after overwrite = %d, want 2 (no duplicate entries)", c.Len())
	}
	mustGet(t, c, "a", "new")

	// "b" is now the LRU entry and must be evicted first.
	c.Set("c", []byte("see"), 0)
	mustMiss(t, c, "b")
	mustGet(t, c, "a", "new")
	mustGet(t, c, "c", "see")
}

func TestSetOverwriteRefreshesTTL(t *testing.T) {
	c, clock := newTestCache(2)

	c.Set("k", []byte("v1"), 10*time.Second)
	clock.Advance(8 * time.Second)
	c.Set("k", []byte("v2"), 10*time.Second) // TTL restarts from here
	clock.Advance(8 * time.Second)

	mustGet(t, c, "k", "v2") // would be expired if the TTL had not refreshed
}

func TestLRUEviction(t *testing.T) {
	c, _ := newTestCache(3)

	c.Set("a", []byte("1"), 0)
	c.Set("b", []byte("2"), 0)
	c.Set("c", []byte("3"), 0)

	mustGet(t, c, "a", "1") // touch "a": order is now b (LRU), c, a (MRU)

	c.Set("d", []byte("4"), 0) // evicts "b"
	mustMiss(t, c, "b")
	mustGet(t, c, "a", "1")
	mustGet(t, c, "c", "3")
	mustGet(t, c, "d", "4")

	if got := c.Stats().Evicted; got != 1 {
		t.Fatalf("Evicted = %d, want 1", got)
	}
}

func TestEvictionPrefersDroppingExpired(t *testing.T) {
	c, clock := newTestCache(2)

	c.Set("old", []byte("x"), 5*time.Second) // LRU, will expire
	c.Set("new", []byte("y"), 0)             // live, LRU among valid entries
	clock.Advance(10 * time.Second)          // "old" is now expired

	c.Set("z", []byte("z"), 0) // must drop expired "old", not evict live "new"

	mustGet(t, c, "new", "y")
	mustGet(t, c, "z", "z")
	if c.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", c.Len())
	}
	s := c.Stats()
	if s.Expired != 1 || s.Evicted != 0 {
		t.Fatalf("Stats = %+v, want Expired=1 Evicted=0", s)
	}
}

func TestTTLExpiry(t *testing.T) {
	c, clock := newTestCache(4)

	c.Set("short", []byte("s"), 10*time.Second)
	c.Set("forever", []byte("f"), 0)

	mustGet(t, c, "short", "s")
	clock.Advance(11 * time.Second)

	mustMiss(t, c, "short") // expired entry is treated as absent
	mustGet(t, c, "forever", "f")

	if c.Len() != 1 { // expired entry removed on access
		t.Fatalf("Len() = %d, want 1 after lazy expiry", c.Len())
	}
}

func TestPurgeExpired(t *testing.T) {
	c, clock := newTestCache(8)

	c.Set("e1", []byte("1"), 5*time.Second)
	c.Set("e2", []byte("2"), 5*time.Second)
	c.Set("keep", []byte("k"), 0)
	clock.Advance(6 * time.Second)

	if removed := c.PurgeExpired(); removed != 2 {
		t.Fatalf("PurgeExpired() = %d, want 2", removed)
	}
	if c.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", c.Len())
	}
	mustGet(t, c, "keep", "k")
	if removed := c.PurgeExpired(); removed != 0 {
		t.Fatalf("second PurgeExpired() = %d, want 0", removed)
	}
}

func TestDelete(t *testing.T) {
	c, _ := newTestCache(4)

	c.Set("a", []byte("1"), 0)
	if !c.Delete("a") {
		t.Fatal("Delete(a) = false, want true")
	}
	mustMiss(t, c, "a")
	if c.Delete("a") {
		t.Fatal("Delete(a) again = true, want false")
	}
	if c.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", c.Len())
	}
}

func TestClear(t *testing.T) {
	c, _ := newTestCache(4)

	c.Set("a", []byte("1"), 0)
	c.Set("b", []byte("2"), 0)
	mustGet(t, c, "a", "1")
	mustMiss(t, c, "nope")

	c.Clear()

	if c.Len() != 0 {
		t.Fatalf("Len() after Clear = %d, want 0", c.Len())
	}
	mustMiss(t, c, "a")
	mustMiss(t, c, "b")

	// Clear empties the store but keeps the cumulative counters.
	s := c.Stats()
	if s.Len != 0 {
		t.Fatalf("Stats().Len = %d, want 0", s.Len)
	}
	if s.Hits == 0 || s.Misses == 0 {
		t.Fatalf("Stats() after Clear = %+v, counters must not reset", s)
	}

	c.Set("c", []byte("3"), 0) // cache stays usable after Clear
	mustGet(t, c, "c", "3")
}

func TestCapacityBoundary(t *testing.T) {
	c, _ := newTestCache(2)

	c.Set("a", []byte("1"), 0)
	c.Set("b", []byte("2"), 0)
	if c.Len() != 2 {
		t.Fatalf("Len() at capacity = %d, want 2", c.Len())
	}
	if got := c.Stats().Evicted; got != 0 {
		t.Fatalf("Evicted at capacity = %d, want 0", got)
	}

	c.Set("c", []byte("3"), 0) // one over capacity: exactly one eviction
	if c.Len() != 2 {
		t.Fatalf("Len() over capacity = %d, want 2", c.Len())
	}
	mustMiss(t, c, "a")
	if got := c.Stats().Evicted; got != 1 {
		t.Fatalf("Evicted = %d, want 1", got)
	}
}

func TestCapacityOne(t *testing.T) {
	c, _ := newTestCache(1)

	c.Set("a", []byte("1"), 0)
	c.Set("b", []byte("2"), 0)
	mustMiss(t, c, "a")
	mustGet(t, c, "b", "2")
	if c.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", c.Len())
	}
}

func TestStats(t *testing.T) {
	c, clock := newTestCache(2)

	c.Set("a", []byte("1"), 0)
	c.Set("b", []byte("2"), 5*time.Second)

	mustGet(t, c, "a", "1") // hit
	mustMiss(t, c, "gone")  // miss
	clock.Advance(10 * time.Second)
	mustMiss(t, c, "b") // expired: miss + expired

	c.Set("c", []byte("3"), 0)
	c.Set("d", []byte("4"), 0) // evicts "a" (LRU among live entries)

	s := c.Stats()
	want := Stats{Hits: 1, Misses: 2, Expired: 1, Evicted: 1, Len: 2}
	if s != want {
		t.Fatalf("Stats() = %+v, want %+v", s, want)
	}
}

func TestConcurrentAccess(t *testing.T) {
	c := New(64)

	const workers = 8
	const opsPerWorker = 500

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < opsPerWorker; i++ {
				key := fmt.Sprintf("key-%d", (id*opsPerWorker+i)%100)
				switch i % 5 {
				case 0:
					c.Set(key, []byte(fmt.Sprintf("v-%d", i)), time.Minute)
				case 1:
					c.Get(key)
				case 2:
					c.Delete(key)
				case 3:
					c.PurgeExpired()
				default:
					c.Stats()
					c.Len()
				}
			}
		}(w)
	}
	wg.Wait()

	if got := c.Len(); got > 64 {
		t.Fatalf("Len() = %d exceeds capacity 64", got)
	}
	s := c.Stats()
	if s.Hits+s.Misses == 0 {
		t.Fatalf("expected some Get traffic, stats = %+v", s)
	}
}

func TestConcurrentSameKeyNoDuplicates(t *testing.T) {
	c := New(4)

	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				c.Set("shared", []byte(fmt.Sprintf("w%d-i%d", id, i)), 0)
				c.Get("shared")
			}
		}(w)
	}
	wg.Wait()

	if c.Len() != 1 {
		t.Fatalf("Len() = %d, want 1: concurrent Set on one key must not duplicate", c.Len())
	}
	if c.lru.Len() != 1 {
		t.Fatalf("lru list length = %d, want 1: list and map out of sync", c.lru.Len())
	}
	if _, ok := c.Get("shared"); !ok {
		t.Fatal("shared key missing after concurrent writes")
	}
}

func TestValueIsCopied(t *testing.T) {
	c, _ := newTestCache(2)

	src := []byte("abc")
	c.Set("k", src, 0)
	src[0] = 'X' // mutating the caller's slice must not affect the cache

	mustGet(t, c, "k", "abc")

	got, _ := c.Get("k")
	got[0] = 'Y' // mutating the returned slice must not affect the cache
	mustGet(t, c, "k", "abc")
}

func TestNewPanicsOnInvalidCapacity(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New(0) did not panic")
		}
	}()
	New(0)
}
