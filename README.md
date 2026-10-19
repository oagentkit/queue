# queue

DB 即队列 + Lease 选主 —— oagentkit 模块矩阵 **tier 0**（基础层）。

```
go get github.com/oagentkit/queue
```

| 包 | 职责 |
|---|---|
| `worker` | 任务表驱动的工作池（DB 即队列）+ `LeaseStore` 选主存储接口 |
| `pglease` | `worker.LeaseStore` 的 PostgreSQL 实现（条件 UPSERT 一行即成） |

## 定位

**tier 0**，零矩阵内依赖 —— 队列与选主的存储契约在此定义，实现方按需提供。

`pglease` 的集成测试是 **DSN 门控** 的（形如 `postgres://…`），
仓内零生产依赖：不设 DSN 时集成测试自动跳过。

## 选主契约

```go
type LeaseStore interface {
    // TryAcquire 原子获取或续约租约。key 空闲/已过期/持有者就是 holder →
    // 置 holder 并顺延 ttl，返回 true；他人持有未过期 → 返回 false。
    TryAcquire(ctx context.Context, key, holder string, ttl time.Duration) (bool, error)
    // Release 释放租约（仅持有者本人生效；优雅停机让位）。
    // 非持有者或 key 不存在 → no-op 返回 nil。
    Release(ctx context.Context, key, holder string) error
}
```

实现方需保证：**同一时刻至多一个 leader**。租约续期失败或进程失联后，
其他实例可在租约过期后接管。

选主回调不在存储接口上，而在 `LeaderElector` 的选项里：

```go
NewLeaderElector(store, key, holder, ttl, interval,
    worker.WithOnGained(func() { /* 首次当选与失而复得均触发 */ }),
    worker.WithOnLost(func()  { /* 续约失败触发；Stop 主动让位不触发 */ }),
)
```

## 开发

```bash
go test ./... -race
go vet ./...
gofmt -l .
```

规范见 [AGENTS.md](AGENTS.md)。

## License

MIT
