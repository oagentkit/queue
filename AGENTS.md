# AGENTS.md — oagentkit/queue

> DB 即队列 + Lease 选主，2 个包：`worker` `pglease`
> module：`github.com/oagentkit/queue` · org：[oagentkit = Open Agent Kit](https://github.com/oagentkit)
> 矩阵分层：**tier 0（基础层）**

## 本仓包含

```
worker/  pglease/
```

> 注意：`pglease` 原本是 `worker/pglease`，拆分后按三层路径方案提到仓根一级，
> module path 是 `github.com/oagentkit/queue/pglease`。

## 硬规则

### 分层与依赖

- 依赖严格自底向上，**禁止反向依赖**。本仓是 tier 0，任何矩阵内模块都不得被本仓依赖。
- 外部依赖：`github.com/oekit/concurrency`（async）、`github.com/oekit/logx`。
- **`github.com/lib/pq` 是测试依赖**（`pglease` 的 DSN 门控集成测试驱动），
  **生产代码禁止 import**。仓内零生产依赖是刻意设计，不要引入。

### 选主语义（正确性红线）

- **`LeaseStore` 必须保证「同一时刻至多一个 leader」。** 这是选主的全部意义。
  实现里的条件 UPSERT 不能退化成「先查后写」—— 那是竞态，不是选主。
- 租约续期失败/进程失联后，其他实例**必须**能在租约过期后接管。
  拿不到租约时**静默退出或让出**，`LeaderElector` 的 `WithOnLost` 回调要真的执行清理。
  ⚠️ 回调在 `LeaderElector` 的选项上（`WithOnGained` / `WithOnLost`），
  **不在 `LeaseStore` 接口上**——`LeaseStore` 只有 `TryAcquire` 与 `Release` 两个方法。
- 选主相关状态必须有 `-race` 回归测试兜底。

### 错误处理

- 一律使用 `github.com/oekit/pkg/errors`；业务错误用 BizError。
- 所有 error 禁止 `_` 忽略。**租约丢失属于必须上报的事件**，不得吞。

### 日志

- 日志走 `github.com/oekit/logx`，组件 logger 用 `logx.Delegating("oagentkit-queue")`，
  禁止 `Default()`/`WithService` 声明包级 logger。
- kv 结构化风格为默认；leader 变更、租约丢失属关键事件，字段需带 lease 名与实例 id。

### 并发安全

- 禁止裸 `go func`：用 `github.com/oekit/concurrency/async` 的 GoSafe 系列。
- 工作池的并发状态用 mutex / atomic 保护；包级全局可变状态用 `atomic.Pointer[T]`。

### API 设计

- 函数选项模式命名 `XxxOption`（禁止裸 `Option`）。
- 导出符号 PascalCase + godoc 注释；包文档放 `doc.go`。

### 发版顺序

本仓的 `CHANGELOG.md` 由 `matrix-tools/scripts/gen_changelogs.py` 从 git 历史生成，
**不要手改**。发版顺序的**单一副本**见 org 规则仓
[CONTRIBUTING §发版](https://github.com/oagentkit/.github/blob/main/CONTRIBUTING.md#发版)：
**预写待发版段 → 提交 CHANGELOG.md → 在那个提交上打 annotated tag**。
tag 必须落在 CHANGELOG 提交**之后** —— 下游 `go get` 拿到的是 tag 快照，
快照里的 CHANGELOG.md 若缺自己那一段，发布说明就是残缺的。

⚠️ 无参重生成 CHANGELOG 会**吞掉待发版段**：生成器只按已存在的 tag 切段，
`--next-version` 是唯一能产出待发版段的开关。**预写之后、打 tag 之前，
不要跑无参重生成。**

本仓是 tier 0 基础层，全矩阵都依赖它 —— tag 快照里的 CHANGELOG 残缺会直接传导到所有下游。

## 明确不属于本仓的

| 能力 | 归属 |
|---|---|
| goroutine 池/单飞/重试/令牌桶 | `github.com/oekit/concurrency` |
| 子进程最小环境与执行封装 | `github.com/oagentkit/runtime/procx` |
| 分片定时任务 | 不在本矩阵；如需请用 `oekit/timertask` |
| 通用 errors/stringx/encoding/mathx | `github.com/oekit/pkg` |
