package ttlcache

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// manualClock 是测试中可手动推进的时钟，避免真实 sleep。
type manualClock struct {
	mu sync.Mutex
	t  time.Time
}

func (m *manualClock) now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.t
}

func (m *manualClock) advance(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.t = m.t.Add(d)
}

// newTestCache 创建使用手动时钟的缓存，返回缓存与时钟。
func newTestCache(capacity int) (*Cache, *manualClock) {
	clock := &manualClock{t: time.Now()}
	c := New(capacity)
	c.now = clock.now
	return c, clock
}

func mustGet(t *testing.T, c *Cache, key, want string) {
	t.Helper()
	v, ok := c.Get(key)
	if !ok {
		t.Fatalf("Get(%q) 未命中，期望值为 %q", key, want)
	}
	if string(v) != want {
		t.Fatalf("Get(%q) = %q，期望 %q", key, v, want)
	}
}

func mustMiss(t *testing.T, c *Cache, key string) {
	t.Helper()
	if v, ok := c.Get(key); ok {
		t.Fatalf("Get(%q) 应未命中，实际得到 %q", key, v)
	}
}

func TestSetGetBasic(t *testing.T) {
	c, _ := newTestCache(4)

	c.Set("a", []byte("1"), 0)
	mustGet(t, c, "a", "1")
	mustMiss(t, c, "nope")

	// Set 复制输入：修改原切片不影响缓存。
	src := []byte("abc")
	c.Set("b", src, 0)
	src[0] = 'X'
	mustGet(t, c, "b", "abc")

	// Get 返回副本：修改返回值不影响缓存。
	v, _ := c.Get("b")
	v[0] = 'Y'
	mustGet(t, c, "b", "abc")

	// 支持任意字节，包括空值与二进制数据。
	c.Set("empty", nil, 0)
	if v, ok := c.Get("empty"); !ok || len(v) != 0 {
		t.Fatalf("空值应命中且长度为 0，得到 %q, ok=%v", v, ok)
	}
	bin := []byte{0x00, 0xff, 0x0a}
	c.Set("bin", bin, 0)
	got, _ := c.Get("bin")
	if string(got) != string(bin) {
		t.Fatalf("二进制数据不一致：%v vs %v", got, bin)
	}
}

func TestSetOverwrite(t *testing.T) {
	c, clock := newTestCache(2)

	c.Set("k", []byte("v1"), time.Minute)
	c.Set("other", []byte("o"), 0)
	c.Set("k", []byte("v2"), 0) // 覆盖：更新值、清除 TTL、刷新 LRU 位置

	if c.Len() != 2 {
		t.Fatalf("覆盖同一 key 后 Len = %d，期望 2（不允许重复条目）", c.Len())
	}
	mustGet(t, c, "k", "v2")

	// TTL 被覆盖为永不过期。
	clock.advance(2 * time.Minute)
	mustGet(t, c, "k", "v2")

	// 覆盖时 k 被刷新为最近使用，再插入新 key 应淘汰 other 而非 k。
	c.Set("third", []byte("3"), 0)
	mustMiss(t, c, "other")
	mustGet(t, c, "k", "v2")
	mustGet(t, c, "third", "3")
}

func TestLRUEviction(t *testing.T) {
	c, _ := newTestCache(2)

	c.Set("a", []byte("1"), 0)
	c.Set("b", []byte("2"), 0)
	c.Get("a") // a 变为最近使用，b 是最久未使用

	c.Set("c", []byte("3"), 0) // 超出容量，应淘汰 b

	mustMiss(t, c, "b")
	mustGet(t, c, "a", "1")
	mustGet(t, c, "c", "3")
	if got := c.Stats().Evicted; got != 1 {
		t.Fatalf("Evicted = %d，期望 1", got)
	}
}

func TestEvictionPrefersExpiredOverValid(t *testing.T) {
	c, clock := newTestCache(2)

	c.Set("old", []byte("1"), time.Minute)
	c.Set("fresh", []byte("2"), 0)
	clock.advance(2 * time.Minute) // old 已过期

	c.Set("new", []byte("3"), 0) // 超容量：先清理过期的 old，而不是淘汰有效记录

	s := c.Stats()
	if s.Expired != 1 || s.Evicted != 0 {
		t.Fatalf("Expired=%d Evicted=%d，期望 1 和 0", s.Expired, s.Evicted)
	}
	mustMiss(t, c, "old")
	mustGet(t, c, "fresh", "2")
	mustGet(t, c, "new", "3")
}

func TestTTLExpiry(t *testing.T) {
	c, clock := newTestCache(4)

	c.Set("short", []byte("s"), time.Minute)
	c.Set("forever", []byte("f"), 0)

	clock.advance(30 * time.Second)
	mustGet(t, c, "short", "s") // 未到期仍命中

	clock.advance(31 * time.Second) // 总计 61s，已过期
	mustMiss(t, c, "short")         // 过期记录在访问时视为不存在
	if got := c.Stats().Expired; got != 1 {
		t.Fatalf("Expired = %d，期望 1", got)
	}
	if got := c.Len(); got != 1 {
		t.Fatalf("过期记录应被移除，Len = %d，期望 1", got)
	}
	mustGet(t, c, "forever", "f") // 无 TTL 记录不受影响

	// 边界：恰好到达过期时刻即视为过期。
	c.Set("edge", []byte("e"), time.Minute)
	clock.advance(time.Minute)
	mustMiss(t, c, "edge")
}

func TestPurgeExpired(t *testing.T) {
	c, clock := newTestCache(8)

	c.Set("e1", []byte("1"), time.Minute)
	c.Set("e2", []byte("2"), time.Minute)
	c.Set("keep", []byte("k"), 0)
	clock.advance(2 * time.Minute)

	if n := c.PurgeExpired(); n != 2 {
		t.Fatalf("PurgeExpired 清理了 %d 条，期望 2", n)
	}
	if got := c.Len(); got != 1 {
		t.Fatalf("Len = %d，期望 1", got)
	}
	mustGet(t, c, "keep", "k")
	if got := c.Stats().Expired; got != 2 {
		t.Fatalf("Expired = %d，期望 2", got)
	}
	if n := c.PurgeExpired(); n != 0 {
		t.Fatalf("再次 PurgeExpired 应清理 0 条，实际 %d", n)
	}
}

func TestDelete(t *testing.T) {
	c, clock := newTestCache(4)

	c.Set("a", []byte("1"), 0)
	if !c.Delete("a") {
		t.Fatal("Delete(存在的 key) 应返回 true")
	}
	if c.Len() != 0 {
		t.Fatalf("删除后 Len = %d，期望 0", c.Len())
	}
	if c.Delete("a") {
		t.Fatal("重复 Delete 应返回 false")
	}
	if c.Delete("missing") {
		t.Fatal("Delete(不存在的 key) 应返回 false")
	}

	// 已过期记录视为不存在：移除并计入 Expired，返回 false。
	c.Set("exp", []byte("x"), time.Minute)
	clock.advance(2 * time.Minute)
	if c.Delete("exp") {
		t.Fatal("Delete(已过期 key) 应返回 false")
	}
	if got := c.Stats().Expired; got != 1 {
		t.Fatalf("Expired = %d，期望 1", got)
	}
	if c.Len() != 0 {
		t.Fatalf("过期记录应被移除，Len = %d，期望 0", c.Len())
	}
}

func TestClear(t *testing.T) {
	c, clock := newTestCache(4)

	c.Set("a", []byte("1"), 0)
	c.Set("b", []byte("2"), time.Minute)
	c.Get("a")
	c.Get("miss")
	clock.advance(2 * time.Minute)
	c.Get("b") // 制造 expired 计数

	c.Clear()

	if c.Len() != 0 {
		t.Fatalf("Clear 后 Len = %d，期望 0", c.Len())
	}
	s := c.Stats()
	if s.Hits != 0 || s.Misses != 0 || s.Expired != 0 || s.Evicted != 0 || s.Items != 0 {
		t.Fatalf("Clear 后统计应全部重置为零，实际 %+v", s)
	}

	// 重置后计数器仍能正常工作。
	mustMiss(t, c, "a")
	if got := c.Stats().Misses; got != 1 {
		t.Fatalf("Clear 后 Misses = %d，期望 1", got)
	}
}

func TestCapacityBoundary(t *testing.T) {
	c, _ := newTestCache(2)

	c.Set("a", []byte("1"), 0)
	c.Set("b", []byte("2"), 0)
	if c.Len() != 2 || c.Stats().Evicted != 0 {
		t.Fatalf("恰好写满容量不应淘汰：Len=%d Evicted=%d", c.Len(), c.Stats().Evicted)
	}

	c.Set("c", []byte("3"), 0) // 第 3 条触发恰好一次淘汰
	if c.Len() != 2 || c.Stats().Evicted != 1 {
		t.Fatalf("超出容量应淘汰一条：Len=%d Evicted=%d", c.Len(), c.Stats().Evicted)
	}

	// 容量为 1 的缓存。
	one, _ := newTestCache(1)
	one.Set("x", []byte("1"), 0)
	one.Set("y", []byte("2"), 0)
	if one.Len() != 1 {
		t.Fatalf("容量 1 时 Len = %d，期望 1", one.Len())
	}
	mustMiss(t, one, "x")
	mustGet(t, one, "y", "2")

	// 非法容量应 panic。
	defer func() {
		if recover() == nil {
			t.Fatal("New(0) 应 panic")
		}
	}()
	New(0)
}

func TestConcurrentAccess(t *testing.T) {
	const (
		goroutines = 8
		keysPerG   = 10
		iterations = 200
		capacity   = 100 // 大于全部不同 key 数（8*10+1=81），保证无淘汰
		sharedKey  = "shared"
	)

	c := New(capacity) // 使用真实时钟，配合 go test -race 检测数据竞争
	var wg sync.WaitGroup

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				key := fmt.Sprintf("g%d-k%d", g, i%keysPerG)
				c.Set(key, []byte{byte(g), byte(i)}, 0)
				c.Get(key)
				c.Get(sharedKey)
				c.Set(sharedKey, []byte{byte(i)}, 0) // 同一 key 并发覆盖更新
				if i%3 == 0 {
					c.Delete(key)
					c.Set(key, []byte{byte(g)}, 0)
				}
			}
		}(g)
	}
	wg.Wait()

	// 不同 key 总数 81，均不应丢失或重复。
	wantItems := goroutines*keysPerG + 1
	if c.Len() != wantItems {
		t.Fatalf("并发写入后 Len = %d，期望 %d（存在重复条目或丢失）", c.Len(), wantItems)
	}
	for g := 0; g < goroutines; g++ {
		for k := 0; k < keysPerG; k++ {
			key := fmt.Sprintf("g%d-k%d", g, k)
			if _, ok := c.Get(key); !ok {
				t.Fatalf("key %q 在并发写入后丢失", key)
			}
		}
	}
	if _, ok := c.Get(sharedKey); !ok {
		t.Fatal("并发更新的 shared key 丢失")
	}

	s := c.Stats()
	if s.Items != wantItems {
		t.Fatalf("Stats.Items = %d，期望 %d", s.Items, wantItems)
	}
	if s.Evicted != 0 || s.Expired != 0 {
		t.Fatalf("不应发生淘汰或过期：Evicted=%d Expired=%d", s.Evicted, s.Expired)
	}
	// 每次 Get 恰好计入 Hits 或 Misses 之一。
	totalGets := uint64(goroutines*iterations*2 + goroutines*keysPerG + 1)
	if s.Hits+s.Misses != totalGets {
		t.Fatalf("Hits+Misses = %d，期望 %d（统计丢失或重复计数）", s.Hits+s.Misses, totalGets)
	}
}

func TestStats(t *testing.T) {
	c, clock := newTestCache(2)

	c.Set("a", []byte("1"), 0)
	c.Set("b", []byte("2"), time.Minute)
	c.Get("a")  // hit
	c.Get("zz") // miss
	clock.advance(2 * time.Minute)
	c.Get("b") // expired
	c.Set("c", []byte("3"), 0)
	c.Set("d", []byte("4"), 0) // 淘汰最久未使用的 a

	s := c.Stats()
	want := Stats{Hits: 1, Misses: 1, Expired: 1, Evicted: 1, Items: 2}
	if s != want {
		t.Fatalf("Stats = %+v，期望 %+v", s, want)
	}
	if s.Items != c.Len() {
		t.Fatalf("Stats.Items = %d 与 Len() = %d 不一致", s.Items, c.Len())
	}
}
