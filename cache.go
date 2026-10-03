// Package ttlcache 提供并发安全的进程内内存缓存，
// 支持最大条目数限制、LRU 淘汰以及每条记录独立的 TTL。
package ttlcache

import (
	"container/list"
	"sync"
	"time"
)

// Stats 是缓存统计信息的快照。
//
// 计数口径：每次 Get 恰好使 Hits、Misses、Expired 之一加一；
// 过期记录被移除（Get/Delete 惰性发现、PurgeExpired 显式清理、
// 或插入时容量清理）都会计入 Expired；
// 只有未过期记录因容量限制被 LRU 淘汰时才计入 Evicted；
// Delete 与 Clear 不计入 Evicted。
type Stats struct {
	Hits    uint64 // Get 命中未过期记录的次数
	Misses  uint64 // Get 时 key 不存在的次数
	Expired uint64 // 因 TTL 过期被移除的记录数
	Evicted uint64 // 因容量限制被淘汰的未过期记录数
	Items   int    // 当前条目数（包含尚未被清理的过期记录）
}

// entry 是缓存内部保存的单条记录。expiresAt 为零值表示永不过期。
type entry struct {
	key       string
	value     []byte
	expiresAt time.Time
}

func (e *entry) expired(now time.Time) bool {
	return !e.expiresAt.IsZero() && !now.Before(e.expiresAt)
}

// Cache 是并发安全的 TTL + LRU 内存缓存，零值不可用，请使用 New 创建。
type Cache struct {
	mu       sync.Mutex
	capacity int
	items    map[string]*list.Element
	lru      *list.List // 前端为最近使用，尾部为最久未使用

	hits    uint64
	misses  uint64
	expired uint64
	evicted uint64

	now func() time.Time // 时间源，测试中可替换
}

// New 创建容量为 capacity 的缓存。capacity 必须为正数，否则 panic。
func New(capacity int) *Cache {
	if capacity <= 0 {
		panic("ttlcache: capacity must be positive")
	}
	return &Cache{
		capacity: capacity,
		items:    make(map[string]*list.Element),
		lru:      list.New(),
		now:      time.Now,
	}
}

// Set 写入 key 对应的值。ttl > 0 时记录在 ttl 之后过期；ttl <= 0 表示永不过期。
//
// 若 key 已存在则原地覆盖其值与 TTL，并将其标记为最近使用，
// 不会产生重复条目。value 会被复制，调用方之后修改原切片不影响缓存。
func (c *Cache) Set(key string, value []byte, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[key]; ok {
		ent := el.Value.(*entry)
		ent.value = append(ent.value[:0], value...)
		ent.expiresAt = c.expiry(ttl)
		c.lru.MoveToFront(el)
		return
	}

	el := c.lru.PushFront(&entry{
		key:       key,
		value:     append([]byte(nil), value...),
		expiresAt: c.expiry(ttl),
	})
	c.items[key] = el
	c.evictIfNeeded()
}

// Get 返回 key 对应的值。记录不存在或已过期时 ok 为 false，
// 过期记录会在此时被移除并计入 Expired 统计。
// 命中未过期记录会将其标记为最近使用。返回值是内部数据的副本。
func (c *Cache) Get(key string) (value []byte, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, found := c.items[key]
	if !found {
		c.misses++
		return nil, false
	}
	ent := el.Value.(*entry)
	if ent.expired(c.now()) {
		c.removeElement(el)
		c.expired++
		return nil, false
	}
	c.hits++
	c.lru.MoveToFront(el)
	return append([]byte(nil), ent.value...), true
}

// Delete 删除指定 key，返回是否删除了有效记录。
// 已过期但尚未清理的记录视为不存在：会被移除并计入 Expired，返回 false。
func (c *Cache) Delete(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.items[key]
	if !ok {
		return false
	}
	if el.Value.(*entry).expired(c.now()) {
		c.removeElement(el)
		c.expired++
		return false
	}
	c.removeElement(el)
	return true
}

// Clear 清空所有条目，并将 Hits/Misses/Expired/Evicted 全部重置为零。
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items = make(map[string]*list.Element)
	c.lru.Init()
	c.hits, c.misses, c.expired, c.evicted = 0, 0, 0, 0
}

// PurgeExpired 显式清理所有已过期记录，返回清理的条数。
// 清理的记录计入 Expired 统计。
func (c *Cache) PurgeExpired() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	removed := 0
	for el := c.lru.Back(); el != nil; {
		prev := el.Prev()
		if el.Value.(*entry).expired(now) {
			c.removeElement(el)
			c.expired++
			removed++
		}
		el = prev
	}
	return removed
}

// Len 返回当前条目数。过期但尚未被清理的记录仍计入，
// 需要精确的有效条目数时可先调用 PurgeExpired。
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// Stats 返回统计信息快照。
func (c *Cache) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Stats{
		Hits:    c.hits,
		Misses:  c.misses,
		Expired: c.expired,
		Evicted: c.evicted,
		Items:   len(c.items),
	}
}

// expiry 把相对 TTL 转换为绝对过期时间；ttl <= 0 表示永不过期。
func (c *Cache) expiry(ttl time.Duration) time.Time {
	if ttl <= 0 {
		return time.Time{}
	}
	return c.now().Add(ttl)
}

// evictIfNeeded 在条目数超过容量时循环清理：从尾部（最久未使用）开始，
// 过期记录直接移除并计入 Expired；遇到的第一条未过期记录被淘汰并计入
// Evicted。调用前必须持有锁。
func (c *Cache) evictIfNeeded() {
	for len(c.items) > c.capacity {
		back := c.lru.Back()
		if back == nil {
			return
		}
		if back.Value.(*entry).expired(c.now()) {
			c.removeElement(back)
			c.expired++
			continue
		}
		c.removeElement(back)
		c.evicted++
	}
}

// removeElement 从 map 与 LRU 链表中移除元素。调用前必须持有锁。
func (c *Cache) removeElement(el *list.Element) {
	delete(c.items, el.Value.(*entry).key)
	c.lru.Remove(el)
}
