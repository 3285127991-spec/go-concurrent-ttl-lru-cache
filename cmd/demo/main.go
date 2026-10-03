// Command demo is a small CLI walkthrough of the ttlcache library.
// It exercises Set/Get/Delete/Clear, LRU eviction, per-entry TTL,
// explicit expiry cleanup and the statistics counters.
package main

import (
	"fmt"
	"time"

	"ttlcache"
)

func main() {
	cache := ttlcache.New(3) // holds at most 3 entries

	fmt.Println("== basic Set/Get ==")
	cache.Set("name", []byte("gopher"), 0) // ttl <= 0: never expires
	cache.Set("lang", []byte("go"), 0)
	printGet(cache, "name")
	printGet(cache, "missing")

	fmt.Println()
	fmt.Println("== overwrite keeps a single entry ==")
	cache.Set("name", []byte("codex"), 0)
	printGet(cache, "name")
	fmt.Printf("entries: %d\n", cache.Len())

	fmt.Println()
	fmt.Println("== LRU eviction at capacity 3 ==")
	cache.Set("a", []byte("1"), 0)
	cache.Set("b", []byte("2"), 0) // cache full: name, a, b (lang is LRU)
	cache.Set("c", []byte("3"), 0) // evicts "lang"
	printGet(cache, "lang")

	fmt.Println()
	fmt.Println("== per-entry TTL ==")
	cache.Set("session", []byte("token-123"), 100*time.Millisecond)
	printGet(cache, "session")
	time.Sleep(150 * time.Millisecond)
	printGet(cache, "session") // expired: treated as absent

	fmt.Println()
	fmt.Println("== explicit cleanup of expired entries ==")
	cache.Set("temp1", []byte("x"), 50*time.Millisecond)
	cache.Set("temp2", []byte("y"), 50*time.Millisecond)
	time.Sleep(80 * time.Millisecond)
	fmt.Printf("purged %d expired entries\n", cache.PurgeExpired())

	fmt.Println()
	fmt.Println("== Delete and Clear ==")
	fmt.Printf("delete c: %v\n", cache.Delete("c"))
	cache.Clear()
	fmt.Printf("entries after Clear: %d\n", cache.Len())

	fmt.Println()
	fmt.Println("== final stats (counters survive Clear) ==")
	s := cache.Stats()
	fmt.Printf("hits=%d misses=%d expired=%d evicted=%d len=%d\n",
		s.Hits, s.Misses, s.Expired, s.Evicted, s.Len)
}

func printGet(cache *ttlcache.Cache, key string) {
	if value, ok := cache.Get(key); ok {
		fmt.Printf("get %-8s -> %s\n", key, value)
		return
	}
	fmt.Printf("get %-8s -> <miss>\n", key)
}
