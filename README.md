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
type LeaseStore interface { /* 条件 UPSERT 拿租约，OnStartedLeading/OnStoppedLeading 回调 */ }
```

实现方需保证：**同一时刻至多一个 leader**。租约续期失败或进程失联后，
其他实例可在租约过期后接管。

## 开发

```bash
go test ./... -race
go vet ./...
gofmt -l .
```

规范见 [AGENTS.md](AGENTS.md)。

## License

MIT
