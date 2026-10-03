# ttlcache — 并发安全的 TTL + LRU 内存缓存（Go）

纯 Go 标准库实现的进程内内存缓存，无任何第三方依赖。按字符串 key 保存任意
字节数据，支持最大条目数限制、LRU 淘汰与每条记录独立 TTL，多个 goroutine
可安全并发读写。

明确**不包含**的功能：磁盘持久化、网络接口、GUI、数据库、Docker 或云服务集成。

## 特性

- `Set` / `Get` / `Delete` / `Clear` 基本操作
- 最大条目数限制，超出时优先清理过期记录，再淘汰最久未访问的有效记录（LRU）
- 每条记录独立 TTL；过期记录在访问时视为不存在，并支持 `PurgeExpired` 显式清理
- 单互斥锁保护全部内部状态，并发安全；同一 key 重复写入原地覆盖，不产生重复条目
- 统计信息：hit / miss / expired / evicted / 当前条目数

## 设计

- **数据结构**：`map[string]*list.Element` + 双向链表（`container/list`）。
  链表前端为最近使用，尾部为最久未使用；`Get`/`Set` 命中时将节点移到前端。
- **并发模型**：一把 `sync.Mutex` 保护 map、链表与统计计数器，所有公开方法
  先加锁再操作，因此同一 key 的并发更新不会产生重复条目或破坏链表状态。
- **过期处理**：惰性过期。记录到期后仍暂存，直到被 `Get`/`Delete` 访问、被
  `PurgeExpired` 显式清理，或在插入触发容量清理时被移除。
- **时间源**：内部使用可替换的时钟（默认 `time.Now`），测试中用手动时钟
  保证 TTL 用例确定性运行，无需真实等待。

## API

```go
c := ttlcache.New(100)                 // 容量 100；capacity <= 0 会 panic

c.Set("k", []byte("v"), time.Minute)   // ttl > 0：1 分钟后过期
c.Set("k", []byte("v"), 0)             // ttl <= 0：永不过期；同 key 原地覆盖
v, ok := c.Get("k")                    // 命中返回副本；不存在或已过期 ok == false
ok = c.Delete("k")                     // 删除有效记录返回 true
n := c.PurgeExpired()                  // 显式清理过期记录，返回清理条数
n = c.Len()                            // 当前条目数（含尚未清理的过期记录）
s := c.Stats()                         // 统计快照：Hits/Misses/Expired/Evicted/Items
c.Clear()                              // 清空条目，并重置全部统计计数器
```

## 语义说明

- **TTL**：`ttl <= 0` 表示永不过期；到达过期时刻即视为过期。过期记录在
  `Get`/`Delete` 时视为不存在并被移除。
- **淘汰**：插入导致超出容量时，从链表尾部开始扫描：过期记录直接移除
  （计入 expired），遇到的第一条未过期记录被淘汰（计入 evicted）。
- **覆盖更新**：同一 key 再次 `Set` 会原地更新值与 TTL，并刷新为最近使用，
  条目数不变。
- **值拷贝**：`Set` 复制输入切片，`Get` 返回内部数据的副本，调用方修改
  任一侧都不会影响缓存内容。
- **统计口径**：每次 `Get` 恰好使 hits / misses / expired 之一加一；
  过期记录被移除（`Get`/`Delete` 惰性发现、`PurgeExpired`、插入时容量清理）
  计入 expired；仅未过期记录因容量限制被淘汰时计入 evicted；`Delete` 与
  `Clear` 不计入 evicted。
- **Clear 与统计**：`Clear` 清空所有条目，**并将 hits / misses / expired /
  evicted 全部重置为零**；重置后计数器继续正常工作。
- **Len**：返回当前存储的条目数，包含已过期但尚未清理的记录；需要精确的
  有效条目数时可先调用 `PurgeExpired`。

## 运行 CLI 演示

```powershell
go run ./cmd/demo
```

依次演示基本读写、覆盖更新、LRU 淘汰、TTL 过期、显式清理、删除与 Clear，
并打印各阶段统计信息。

## 运行测试

```powershell
go test ./...            # 运行全部测试
go test -v ./...         # 查看每个用例
go test -count=1 ./...   # 禁用测试缓存，强制重新运行
go test -cover ./...     # 查看覆盖率
```

测试覆盖：基本读写、覆盖更新、LRU 淘汰、淘汰时优先清理过期记录、TTL 过期
（含到期边界）、显式清理、删除、Clear、容量边界（写满不淘汰 / 容量 1 /
非法容量 panic）、并发访问（8 goroutine 混合读写删，校验无重复条目、无丢失、
统计一致）以及统计信息精确计数。

如本机装有 gcc（Windows 上可用 MinGW-w64），可启用竞态检测器进一步验证
并发安全（`-race` 需要 CGO）：

```powershell
go test -race -count=1 ./...
```

## 项目结构

```
cache.go          核心缓存库（package ttlcache）
cache_test.go     自动化测试
cmd/demo/main.go  命令行演示程序
```
