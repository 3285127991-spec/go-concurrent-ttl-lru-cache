// demo 是 ttlcache 的命令行演示程序，依次展示基本读写、
// 覆盖更新、LRU 淘汰、TTL 过期、显式清理、删除与 Clear。
package main

import (
	"fmt"
	"time"

	"ttlcache"
)

func printStats(c *ttlcache.Cache) {
	s := c.Stats()
	fmt.Printf("    统计: hits=%d misses=%d expired=%d evicted=%d items=%d\n",
		s.Hits, s.Misses, s.Expired, s.Evicted, s.Items)
}

func main() {
	c := ttlcache.New(3)

	fmt.Println("== 1. 基本读写（容量 3） ==")
	c.Set("name", []byte("gopher"), 0) // ttl <= 0 表示永不过期
	c.Set("lang", []byte("go"), 0)
	if v, ok := c.Get("name"); ok {
		fmt.Printf("    Get(name) = %q\n", v)
	}
	if _, ok := c.Get("missing"); !ok {
		fmt.Println("    Get(missing) = 未命中")
	}
	printStats(c)

	fmt.Println("== 2. 覆盖更新同一 key（不产生重复条目） ==")
	c.Set("name", []byte("codex"), 0)
	if v, ok := c.Get("name"); ok {
		fmt.Printf("    Get(name) = %q, Len = %d\n", v, c.Len())
	}

	fmt.Println("== 3. LRU 淘汰（容量 3，写入第 4 条） ==")
	c.Get("lang") // 访问 lang，使 name 变为最久未使用
	c.Set("os", []byte("windows"), 0)
	c.Set("arch", []byte("amd64"), 0) // 触发淘汰 name
	if _, ok := c.Get("name"); !ok {
		fmt.Println("    name 已被 LRU 淘汰")
	}
	printStats(c)

	fmt.Println("== 4. TTL 过期（100ms 后过期） ==")
	c.Set("session", []byte("token-123"), 100*time.Millisecond)
	time.Sleep(150 * time.Millisecond)
	if _, ok := c.Get("session"); !ok {
		fmt.Println("    session 已过期，Get 视为不存在并被移除")
	}
	printStats(c)

	fmt.Println("== 5. 显式清理过期项 PurgeExpired ==")
	c.Set("tmp1", []byte("x"), 50*time.Millisecond)
	c.Set("tmp2", []byte("y"), 50*time.Millisecond)
	time.Sleep(80 * time.Millisecond)
	fmt.Printf("    清理了 %d 条过期记录\n", c.PurgeExpired())
	printStats(c)

	fmt.Println("== 6. 删除与 Clear ==")
	fmt.Printf("    Delete(arch) = %v\n", c.Delete("arch"))
	fmt.Printf("    Delete(arch) = %v（重复删除）\n", c.Delete("arch"))
	c.Set("reset-demo", []byte("1"), 0)
	c.Get("reset-demo")
	c.Clear()
	fmt.Println("    Clear 后：条目与统计计数器全部重置")
	printStats(c)
}
