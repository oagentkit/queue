# queue CHANGELOG

> 本文件由 `matrix-tools/scripts/gen_changelogs.py` 从 git 历史生成，按 tag 切段、取每段内的 conventional commit 渲染。

## [v0.6.1] — 2026-10-20

### 修复(docs)：LeaseStore 契约写成真实方法集，选主回调归位 LeaderElector 选项 (#3)

Co-authored-by: Claude Opus 4.8 (1M context) <noreply@anthropic.com>

## [v0.6.0] — 2026-10-19

- 杂务(deps)：依赖地板对齐远端最新（concurrency v0.17.1 / logx v0.7.1） (#13)

## [v0.5.0] — 2026-10-19

- 杂务(deps)：oekit 依赖升级（concurrency v0.16.0→v0.17.0）

## [v0.4.0] — 2026-10-18

### 新增：queue 仓建立——DB 即队列 + Lease 选主（2 包）

从 agentkit 单仓切出 tier0 基础层模块，module 路径 github.com/oagentkit/queue。
收编 2 个包：
- worker   任务表驱动的工作池（DB 即队列）+ LeaseStore 选主存储接口
- pglease  worker.LeaseStore 的 PostgreSQL 实现（条件 UPSERT 一行即成）
路径调整：原 worker/pglease 提到仓根一级，module path 变为 github.com/oagentkit/queue/pglease
（遵循矩阵「包名保留、只加模块前缀」的三层路径方案）。
外部依赖 github.com/oekit/concurrency + github.com/oekit/logx；
github.com/lib/pq 仅为 DSN 门控集成测试驱动，仓内零生产依赖（AGENTS.md 已立为红线）。
AGENTS.md 立了选主正确性红线：LeaseStore 必须保证至多一个 leader，
条件 UPSERT 不得退化成「先查后写」的竞态。
门禁：go build / go vet / go test -race 2 包全绿，gofmt 干净。

- 杂务(deps)：oekit 依赖升级 (#1)
- 杂务(deps)：oekit/concurrency v0.12.0 -> v0.13.1
- 杂务(deps)：oekit 三模块升至最新（pkg v0.13.0 / concurrency v0.12.0 / logx v0.7.0）
- 杂务(deps)：oekit/concurrency v0.6.0 -> v0.9.0

- CI：接入 org 共享门禁（规则本体在 oagentkit/.github）
