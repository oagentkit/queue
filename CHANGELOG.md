# queue CHANGELOG

> 本文件由 `matrix-tools/scripts/gen_changelogs.py` 从 git 历史生成，按 tag 切段、取每段内的 conventional commit 渲染。

## [v0.6.3] — 2026-10-21

- 杂务(deps)：依赖地板追平上游本轮新版本——oekit/concurrency v0.19.0

## 下游要做什么
**`go mod tidy`**。地板抬高对下游是隐式 breaking：不跑就报
`updates to go.mod needed`，且报错不提真正原因（**不是本仓的 API 变了**）。
本仓**自身代码零改动**——只看本仓的 diff 看不出这是一次地板抬高。
## 抬了哪几块、为什么无行为差异
**`github.com/oekit/concurrency` v0.18.0 → v0.19.0**
上游 diff 只动了 2 个包：
- `retry`：**抖动 panic 修复**——`rand.Int63n` 的契约是 n > 0，而「退避幅度为 0」
  是生产可达路径（cfg.Delay=0 / DelayHint 返回 0 / float64→int64 溢出成负数），
  旧实现在这些路径上直接 panic。改为钳到 [0, MaxInt64]。
- `syncx`：新增全局在飞闸门原语（`Gate`）+ 说明同步。
**`breaker` 与 `async` 两个包一字未动。**
**本仓实际 import 的 `oekit/concurrency` 子包：async **
与上游改动相交的：**（无）**
## 门禁
```
go build / vet / test -race   ✅
gofmt -l .                    ✅ 空
```

- 杂务(deps)：oekit 依赖地板抬到 v0.18.0

oekit 姊妹矩阵的依赖地板对齐到最新：
- github.com/oekit/pkg        v0.17.0 → v0.18.0
- github.com/oekit/concurrency v0.17.1 → v0.18.0
- github.com/oekit/logx       已是最新 v0.7.1，不动
oekit v0.18.0 的两个发布都是**纯增量**，无破坏性变更：
- pkg：jsonrepair 补抽数组原语 ExtractArray；
- concurrency：新增 `DoValue[T]`（带返回值的重试），`Do` 改为委托给它——
  退避序列、抖动、MaxDelay 封顶、DelayHint 优先、Retryable 门控、ctx 取消
  **只有一份实现**（此前这类调用方只能自己重写一遍退避，且常丢掉 DelayHint 语义）。
依据 oagentkit/matrix-tools#44 的还债流程：依赖地板对齐到发版时刻的最新。
本仓本轮不发 tag（不在发版集内），地板在 main 上就位，下次发版自然带上。


- 文档(AGENTS)：补「发版顺序」小节，指向 org CONTRIBUTING 单一副本

13 个业务仓的 AGENTS.md 此前对发版顺序零覆盖（只有 textx 有个
「### 版本与发版」标题，但里面只讲 SemVer 与 module 路径），
发版顺序只写在 org 规则仓的 .github/CONTRIBUTING.md 里，单仓读者看不到。
本次只加**指针 + 本仓相关提醒**，runbook 本体保持**单一副本**
（CONTRIBUTING §发版），避免 13 份副本各自漂移。
覆盖四条：
- CHANGELOG.md 由 matrix-tools/scripts/gen_changelogs.py 从 git 历史生成，不要手改
- tag 必须落在 CHANGELOG 提交之后（下游 go get 拿到的是 tag 快照）
- 警告：无参重生成会吞掉待发版段，--next-version 是唯一能产出待发版段的开关
- 本仓相关提醒（见正文）


## [v0.6.2] — 2026-10-20

### 修复(go)：Go 地板降到 go 1.24.0，CI go-version 同步到 1.24

承接 matrix-tools#22（tier0 三仓的地板被无谓抬高）——本轮把 tier1+ 里
**依赖链全兼容**的仓也降下来（matrix-tools#34 的排查结论）。
## 依据
- **依赖链上没有要求更高版本的包**：本仓的每个依赖（直接与间接）的 go 指令
  都 ≤ 1.24.x，本仓声明 1.26.0 没有任何依赖需要它。
- **代码本身兼容 1.24**：真实 go1.24.0 工具链（`GOTOOLCHAIN=go1.24.0`）
  build + test 全绿——实跑，不是推断。
- **门槛是 go 指令不是 toolchain**（编译器实证）：`go 1.28.0` 被拒，
  `go 1.24.0` + `toolchain go1.28.0` 通过。
## toolchain 行的取舍：删掉
toolchain **不构成门槛**，但 `toolchain go1.27.2` 会让 `go` 命令**自动下载**
1.27.2，与「最低支持版本是 1.24」相悖。删掉，也与基座 oekit 六个基础仓一致。
## 依赖地板同步重钉
降 go 指令后，`require` 必须指向**已发新 tag 的 tier0 仓**（它们的 tag 快照
里才是 go 1.24.0）——否则 MVS 会拉到旧 tag 的 1.26 地板，降级形同虚设。
本仓的内部 require 已随 `go mod tidy` 一并抬到对应的新 tag。
## 改动
- `go.mod`：`go 1.26.0` → `go 1.24.0`；删 `toolchain go1.27.2`；
  内部 require 抬到 tier0 新 tag
- `.github/workflows/ci.yml`：`go-version: '1.26'` → `'1.24'`
CI 的 go-version 与 go.mod 的 go 指令**成对**——这是跨层通用纪律，不因本次
降级而放松。CI 用高版本构建、go.mod 承诺低版本时，CI 全绿但用户按最低版本
编译失败，门禁绿了而承诺没兑现。
## 验证
- `GOTOOLCHAIN=go1.24.0 go build ./...` → exit 0
- `GOTOOLCHAIN=go1.24.0 go test ./...` → 全绿
- `go mod tidy` → 零差异
门禁见 oagentkit/matrix-tools：`scripts/verify_go_floor.py`（判据 4 覆盖
tier1+ 相对地板，判据独立不复用任何生成器）。

## [v0.6.1] — 2026-10-20

### 修复(docs)：LeaseStore 契约写成真实方法集，选主回调归位 LeaderElector 选项

Co-authored-by: Claude Opus 4.8 (1M context) <noreply@anthropic.com>

## [v0.6.0] — 2026-10-19

- 杂务(deps)：依赖地板对齐远端最新（concurrency v0.17.1 / logx v0.7.1）

matrix-tools 的 verify_dep_floors.py 首次全矩阵扫描查出：43 条第一方依赖里
28 条落后，横跨 11 个仓；本仓是其中之一。
所升版本与本仓原钉版本相比源码零改动，逐版实测确认：
- textx v0.3.0→v0.4.0 / obs v0.4.1→v0.5.0 / runtime v0.5.0→v0.6.0 /
  mcpkit v0.5.1→v0.6.0  全树逐字节相同（diff -rq 退出码 0）
- oekit/pkg v0.14.0→v0.15.1  仅新增 urlguard 包（纯标准库，pkg 的 go.mod
  连 require 块都没有，零新增三方依赖）
- oekit/concurrency v0.17.0→v0.17.1  仅三个文档文件
- oekit/logx v0.7.0→v0.7.1  仅 CI 配置与文档
无 API 变更、无兼容风险。
本地门禁：go mod tidy -diff 无输出 / go build / go vet / gofmt / go test -race 全通过。


## [v0.5.0] — 2026-10-19

- 杂务(deps)：oekit 依赖升级（concurrency v0.16.0→v0.17.0）

本 PR 含两项改动。
## 1. oekit 依赖地板
姊妹矩阵 oekit 的 `concurrency` 出了 v0.17.0，依赖地板一并对齐。
升级前已核对 `oekit/concurrency` v0.16.0...v0.17.0 的全部 2 个提交：
- `feat(ratelimit): KeyedLimiter.Create 护栏内原子登记 (Fixes #33)` — `ratelimit/keyed.go` +28 行
- `docs: 矩阵总览表 DESC 口径同步` — 仅 AGENTS.md / README.md / CHANGELOG.md
无 breaking / remove 标记，未触及任何已有导出签名，故为纯补丁式升级。
## 2. 迁出被 force 移动过的 tag
本仓原先依赖的姊妹仓 tag（发布后被强推到了重写后的 main）内容已与
sum.golang.org 记录不一致，于是任何走 `proxy.golang.org` 或 `GOPROXY=direct`
的构建都会 SECURITY ERROR。本地往往不报错——模块缓存预热 + goproxy.cn
还留着移动前的字节，把问题掩盖了。
已为 8 个模块重打不可变的**附注 tag**（内容与被移动 tag 指向同一 commit，
只换版本号，让校验和数据库重新记录一次正确哈希），并把依赖迁过去。
`go.sum` 的 h1 全部取自 sum.golang.org，仅就地替换目标模块的两行
（zip h1 + go.mod h1），其余行不动。


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

- 杂务(deps)：oekit 依赖升级

concurrency v0.13.1→v0.16.0
姊妹矩阵 oekit 的依赖地板此前被漏掉（上一轮只扫了 oagentkit 内部依赖），本次一并对齐到最新。
升级前已确认 oekit/concurrency v0.13.1..v0.16.0 的 8 个提交全是 feat/docs/chore，
无 breaking/remove 标记。其中 #28 订正了 `WithCooldown(0)` 的语义——
即 llms.txt 里警告过的那个陷阱，升上去正好拿到修正后的行为。
go.sum 的 h1 取自 sumdb，仅就地替换目标 mod 的两行、其余行不动。

- 杂务(deps)：oekit/concurrency v0.12.0 -> v0.13.1

本仓不使用上游 breaker（熔断只在 llm 仓），v0.13.0 的 #25 修复与
v0.13.1 的 #26（RetryableWithHeaders 头值 OWS 修剪）对本仓无直接影响，
本次为矩阵内版本对齐。
门禁：go mod tidy -diff / build / vet /
test -race / gofmt 全绿。

- 杂务(deps)：oekit 三模块升至最新（pkg v0.13.0 / concurrency v0.12.0 / logx v0.7.0）

拿到上游新增能力：
- concurrency #22 RetryableWithHeaders：403 头感知判定（次级限流 / 主限流耗尽）
- concurrency #23 FanInLimit：受限并发扇出
- concurrency #24 WithTokenClock、#21 Lockout（keyed lockout/penalty）
- logx #12 TraceHandler：给裸 slog.Handler 注入 trace_id
破坏性变更核对（均无影响）：
- logx v0.7.0 ParseLogLevel 签名改为 (Level, error) —— 我们未使用该函数
- concurrency v0.9.0 的 Slot.Panicked 废弃 —— rag 已迁到 Slot.Err + errors.As
门禁：go build / go vet / go test -race 全绿。

- 杂务(deps)：oekit/concurrency v0.6.0 -> v0.9.0

拿到上游显式三态状态机（#17）与 singleflight 可观测折叠（#18），并为本次
breaker 替换评估了语义差异。
用到的子包只有 async / singleflight / syncx —— 上游 breaker 包我们并未引用
（runtime/breaker 是本仓自己的三态实现），所以本次升级零 breaking。
关于 breaker 替换的现状：#17 已落地（closed/open/half-open + 代绑定 + 探测时限 +
开放期失败不延长冷却），但两侧的触发语义仍不对齐，已在上游补两个需求：
- #19 冷却期与探测失联时限共用 probeTimeout，无法独立配置
- #20 缺「连续失败 N 次即熔断」（trip）触发
因此本包暂不删除 —— llm.TestResilientBreakerTrips 钉的是 trip 语义，
上游 v0.9.0 只有速率型触发（dropRatio > 0），现在换会让该测试失败。


- CI：接入 org 共享门禁（规则本体在 oagentkit/.github）
