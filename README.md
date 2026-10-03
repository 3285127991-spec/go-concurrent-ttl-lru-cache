# go-concurrent-ttl-lru-cache

一个纯 Go 标准库实现的并发安全内存缓存：最大条目数限制 + LRU 淘汰 + 每条记录独立 TTL。
仅包含后端库、命令行演示和自动化测试，不依赖数据库、Docker、云服务或任何第三方包。

## 功能特性

- 字符串 key，值为任意字节切片（`[]byte`），存入和取出时均会拷贝，外部修改不影响缓存
- `Set` / `Get` / `Delete` / `Clear` 基础操作
- 最大条目数限制，超出时淘汰最久未访问（LRU）的有效记录
- 每条记录独立 TTL；`ttl <= 0` 表示永不过期
- 过期记录在 `Get` 时视为不存在并被惰性移除；`PurgeExpired` 支持显式清理
- 所有方法可被多个 goroutine 安全并发调用；同一 key 的并发更新不会产生重复条目
- 统计信息：命中、未命中、过期、淘汰、当前条目数

## 快速开始

```powershell
# 运行测试（可重复执行）
go test ./...

# 详细输出
go test -v ./...

# 竞态检测（需要 CGO 与 gcc 工具链，可选）
go test -race ./...

# 运行 CLI 演示
go run ./cmd/demo

# 静态检查
go vet ./...
```

要求：Go 1.27+（见 `go.mod`），Windows / Linux / macOS 均可运行。

## 使用示例

```go
package main

import (
	"fmt"
	"time"

	"ttlcache"
)

func main() {
	c := ttlcache.New(1000) // 最多 1000 条

	c.Set("key", []byte("value"), time.Minute) // 1 分钟后过期
	c.Set("forever", []byte("x"), 0)           // ttl <= 0：永不过期

	if v, ok := c.Get("key"); ok {
		fmt.Println(string(v))
	}

	c.Delete("key")
	fmt.Println(c.PurgeExpired()) // 显式清理所有过期记录
	fmt.Printf("%+v\n", c.Stats())
	c.Clear()
}
```

## API 一览

| 方法 | 说明 |
| --- | --- |
| `New(capacity int) *Cache` | 创建缓存；`capacity < 1` 会 panic |
| `Set(key string, value []byte, ttl time.Duration)` | 写入；已存在的 key 原地更新并刷新 TTL，不产生重复条目 |
| `Get(key string) ([]byte, bool)` | 读取并标记为最近使用；过期记录视为不存在并被移除 |
| `Delete(key string) bool` | 删除指定 key，返回是否存在 |
| `Clear()` | 清空全部条目 |
| `PurgeExpired() int` | 显式清理所有过期条目，返回清理数量 |
| `Len() int` | 当前条目数（含尚未清理的过期条目） |
| `Stats() Stats` | 统计快照：`Hits` / `Misses` / `Expired` / `Evicted` / `Len` |

## 语义说明

- **LRU 淘汰**：每次 `Get` 命中或 `Set` 都会把该 key 标记为最近使用。容量满时写入新 key，
  会从最久未访问的一端扫描：沿途的过期条目直接移除（计入 `Expired`），
  遇到的第一条**有效**记录被淘汰（计入 `Evicted`），即优先淘汰最久未访问的有效记录。
- **TTL**：过期判定在访问时进行（惰性过期），过期条目在 `Get` 时按不存在处理；
  在 `PurgeExpired` 调用或淘汰扫描前，过期条目仍占用 `Len` 计数。
- **统计口径**：`Hits + Misses` 等于 `Get` 调用总次数；访问到过期条目同时计入 `Misses` 和 `Expired`；
  `Expired` 统计所有因 TTL 到期被移除的条目（无论由 `Get`、`PurgeExpired` 还是淘汰扫描触发）；
  `Evicted` 只统计为腾空间而被淘汰的有效条目。
- **Clear 与统计（明确约定）**：`Clear` **只清空条目**，`Len` 归零；
  `Hits` / `Misses` / `Expired` / `Evicted` 为生命周期累计计数器，**不会**被 `Clear` 重置。

## 并发与实现

- 单把 `sync.Mutex` 保护 `map[string]*list.Element` 索引与双向链表（`container/list`），
  链表头部为最近使用。所有操作在锁内完成，无锁外状态变更。
- 同一 key 的 `Set` 走"原地更新 + 移动到头部"路径，map 与链表始终一一对应，
  并发写同一 key 不会产生重复条目或破坏链表（有对应测试）。
- 未实现（按需求明确排除）：磁盘持久化、网络接口、GUI、数据库集成。

## 项目结构

```
cache.go          核心库（package ttlcache）
cache_test.go     自动化测试
cmd/demo/main.go  命令行演示
go.mod            模块定义（无第三方依赖）
```

## 测试覆盖

`go test ./...` 覆盖：基本读写、覆盖更新（含 TTL 刷新）、LRU 淘汰顺序、
淘汰时优先清理过期项、TTL 过期、显式清理、删除、Clear、容量边界（含容量为 1）、
并发读写、并发写同一 key 不产生重复条目、统计信息、返回值拷贝隔离、非法容量 panic。

TTL 相关测试使用注入的假时钟，确定性强、无真实等待，可反复运行。
