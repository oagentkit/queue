# queue CHANGELOG

> 本文件由 `matrix-tools/scripts/gen_changelogs.py` 从 git 历史生成，按 tag 切段、取每段内的 conventional commit 渲染。

## [v0.11.3] — 2026-11-12

- 杂务(deps)：依赖地板追到最新——github.com/oekit/logx v0.10.0

| 依赖 | 现地板 | 抬到 |
|---|---|---|
| `github.com/oekit/logx` | `v0.9.2` | `v0.10.0` |
## 为什么
矩阵的**发版时刻地板时效**门禁（`verify_tag_floor --ref HEAD`）要求：各仓 HEAD
（= 下次发版会发出去的内容）的依赖地板，相对那一刻的依赖最新版本，不能已落后。
oekit 基座与矩阵模块都在高频发版 ⇒ 地板是**跑步机**，落后一格就会判红。
## 兼容性实测
本工具在提交前跑完 `go build` / `go vet` / `go test -race -count=1 ./...`（全绿才提交）
与 `go mod tidy -diff`（无漂移）⇒ 改动**只影响依赖版本**，零行为差异。
只改 `go.mod` / `go.sum` ⇒ 不需要 CHANGELOG 条目（版本段由下次发版的 `prewrite` 生成）。

- 杂务(modernize)：sort.* → slices.*/min-max 内建——吃 Go 1.21+ 标准库，零行为变化

- CI：go-version 1.24 → 1.26——go 指令已被依赖地板顶至 1.26.0（oekit/pkg v0.45.0 要求），薄调用层同步

## [v0.11.2] — 2026-11-11

### 修复(worker)：Stop→Start 重启竞态 + 错误字段标准名（#103 #104）

- #103 Stop 等满「done + quit」双信号：quit 在 running 清零后的最后一步关闭
  （defer 注册最先 → 执行最后）。close(done)（finishCampaign 尾部）与
  running 清零之间的窗口曾让 Stop 返回后紧接的 Start 命中 already_running
  被静默吞掉——本副本不再参选、租约被他副本接管（#96 修复后的 defer 次序
  残余竞态）。50 轮 Stop→Start 循环回归钉住
- #104 worker 侧 5 处错误字段从手写 "error" 字面量改 logx.ErrKey（"err"）——
  两矩阵落同一 Loki/ES，查询侧不再 OR 双写
测试按 issue 号命名新增 2 条回归用例；go test -race 全绿。

- 杂务(deps)：依赖地板追到最新——github.com/oekit/concurrency v0.33.0

| 依赖 | 现地板 | 抬到 |
|---|---|---|
| `github.com/oekit/concurrency` | `v0.32.0` | `v0.33.0` |
## 为什么
矩阵的**发版时刻地板时效**门禁（`verify_tag_floor --ref HEAD`）要求：各仓 HEAD
（= 下次发版会发出去的内容）的依赖地板，相对那一刻的依赖最新版本，不能已落后。
oekit 基座与矩阵模块都在高频发版 ⇒ 地板是**跑步机**，落后一格就会判红。
## 兼容性实测
本工具在提交前跑完 `go build` / `go vet` / `go test -race -count=1 ./...`（全绿才提交）
与 `go mod tidy -diff`（无漂移）⇒ 改动**只影响依赖版本**，零行为差异。
只改 `go.mod` / `go.sum` ⇒ 不需要 CHANGELOG 条目（版本段由下次发版的 `prewrite` 生成）。

- 杂务(deps)：依赖地板追到最新——github.com/oekit/concurrency v0.32.0

| 依赖 | 现地板 | 抬到 |
|---|---|---|
| `github.com/oekit/concurrency` | `v0.31.2` | `v0.32.0` |
| `github.com/oekit/logx` | `v0.9.1` | `v0.9.2` |
## 为什么
矩阵的**发版时刻地板时效**门禁（`verify_tag_floor --ref HEAD`）要求：各仓 HEAD
（= 下次发版会发出去的内容）的依赖地板，相对那一刻的依赖最新版本，不能已落后。
oekit 基座与矩阵模块都在高频发版 ⇒ 地板是**跑步机**，落后一格就会判红。
## 兼容性实测
本工具在提交前跑完 `go build` / `go vet` / `go test -race -count=1 ./...`（全绿才提交）
与 `go mod tidy -diff`（无漂移）⇒ 改动**只影响依赖版本**，零行为差异。
只改 `go.mod` / `go.sum` ⇒ 不需要 CHANGELOG 条目（版本段由下次发版的 `prewrite` 生成）。

- 杂务(deps)：依赖地板追到最新——github.com/oekit/concurrency v0.31.2

| 依赖 | 现地板 | 抬到 |
|---|---|---|
| `github.com/oekit/concurrency` | `v0.31.1` | `v0.31.2` |
| `github.com/oekit/logx` | `v0.9.0` | `v0.9.1` |
## 为什么
矩阵的**发版时刻地板时效**门禁（`verify_tag_floor --ref HEAD`）要求：各仓 HEAD
（= 下次发版会发出去的内容）的依赖地板，相对那一刻的依赖最新版本，不能已落后。
oekit 基座与矩阵模块都在高频发版 ⇒ 地板是**跑步机**，落后一格就会判红。
## 兼容性实测
本工具在提交前跑完 `go build` / `go vet` / `go test -race -count=1 ./...`（全绿才提交）
与 `go mod tidy -diff`（无漂移）⇒ 改动**只影响依赖版本**，零行为差异。
只改 `go.mod` / `go.sum` ⇒ 不需要 CHANGELOG 条目（版本段由下次发版的 `prewrite` 生成）。


## [v0.11.1] — 2026-11-10

### 修复(worker,sqlqueue)：选主重启状态机 / Pool panic 可恢复 / claim attempts 真实落库（#96 #97 #98）

fix(sqlqueue): ClaimNextPendingDetailed 返回真实落库 attempts，修复与并发复位的偏低报竞态（#98）
旧实现返回「候选 SELECT 快照 +1」，隐含断言「SELECT 与守卫 UPDATE 之间
pending 行的 attempts 不变，能改它的只有会令本调用守卫失败的 claim 类
UPDATE」——该断言不成立：ResetRunningToPending 也做 attempts+1 且把
running 行改回 pending，并发的「他副本 claim → 失联复位」复合序列落进
SELECT→UPDATE 窗口时，本调用的守卫 UPDATE 仍然命中（行又变回 pending），
返回值却比库内实际值少 2。attempts 是宿主毒任务闸门的输入
（worker.DetailedClaimer），系统性偏低报 ⇒ 闸门晚触发、毒任务超额执行。
- PG/SQLite：守卫 UPDATE 改为 UPDATE ... RETURNING attempts，自增与读取
  同语句原子完成，返回值不再依赖任何窗口假设；
- MySQL（无 RETURNING）：守卫 UPDATE 命中后立即重读同一行——行刚被本
  调用置 running、heartbeat_at 写为当前时钟，复位判据在语句级粒度内不可
  能命中、他副本 claim 守卫只认 pending，重读窗口内无写者；
- 修正 ClaimNextPendingDetailed 文档里被证伪的窗口断言，如实描述竞态与
  新取值方式。
回归测试：
- TestClaimAttemptsMatchesPersistedValueUnderResetRace 并发压测
  （4 领取方 + 2 复位方 + 心跳回拨方，-race 下数秒）：对同一行的
  reported attempts 序列必须严格递增——attempts 只增不减，修复后返回值
  取自守卫 UPDATE 自身天然成立；缺陷下 SELECT 快照可早于上次 claim 的
  写入，序列持平/倒退（修复前实测 683 次非严格递增）。任何一次违规都是
  「返回值 ≠ 落库值」的实锤，正向观测（返回后的合法并发写）不作数。
- TestClaimAttemptsSequentialExactness 顺序对照：claim/复位交错下返回值
  逐次等于库内值（1/3/5/7，计数口径不变）。
Closes oagentkit/queue#98

- 杂务(deps)：依赖地板追到最新——github.com/oekit/concurrency v0.31.1

| 依赖 | 现地板 | 抬到 |
|---|---|---|
| `github.com/oekit/concurrency` | `v0.31.0` | `v0.31.1` |
| `github.com/oekit/logx` | `v0.8.12` | `v0.9.0` |
## 为什么
矩阵的**发版时刻地板时效**门禁（`verify_tag_floor --ref HEAD`）要求：各仓 HEAD
（= 下次发版会发出去的内容）的依赖地板，相对那一刻的依赖最新版本，不能已落后。
oekit 基座与矩阵模块都在高频发版 ⇒ 地板是**跑步机**，落后一格就会判红。
## 兼容性实测
本工具在提交前跑完 `go build` / `go vet` / `go test -race -count=1 ./...`（全绿才提交）
与 `go mod tidy -diff`（无漂移）⇒ 改动**只影响依赖版本**，零行为差异。
只改 `go.mod` / `go.sum` ⇒ 不需要 CHANGELOG 条目（版本段由下次发版的 `prewrite` 生成）。


## [v0.11.0] — 2026-11-09

### 修复(pglease)：fencing token 跨行生命周期存续（fenced 行墓碑/免删）+ elector 路径易主推高 generation（#89 #90）

- cca020f

### 修复：queue 第三轮 issue 清零（#83 #84 #85 #86 #87 #88）

* fix(sqllease): 长度护栏与过期租约清理移植到三方言实现（#83 #84）
- #83：MaxKeyLen/MaxHolderLen 常量与 Migrate 的 VARCHAR 同源派生，
  validateIdentity 补 rune 口径长度校验（TryAcquire/Release 共用），
  ErrKeyTooLong/ErrHolderTooLong 哨兵——pglease#34 的三方言移植；
- #84：新增 Cleanup（DB 时钟判据、RowsAffected 透传），方言时钟判据
  以 ExpiredPredicate() 公共出口单源派生（TryAcquire/Cleanup/定向清理
  同钟）；worker 增 LeaseCleaner 可选接口（类型断言探测，不改
  LeaseStore），sqllease/pglease 两参考实现编译期钉住；
- 测试：recording stub driver 三方言矩阵（哨兵+driver 零调用+边界放行
  +rune 口径；DELETE 形态+严格小于+无占位符+行数透传）。
* fix(sqlqueue): 构造期拦超长 runnerID、Enqueue 哨兵拦超长 taskID（#85）
New 此前只拦空 runnerID（#69/#43），长度不设防：Migrate 声明
task_id VARCHAR(64)/runner_id VARCHAR(128)，超长值原样交 driver——
PG/MySQL strict 报 driver 通用错误（runnerID 那条报错点在首次 claim
的后台循环），MySQL 非 strict 静默截断（任务塌行/副本互认身份），
SQLite 原样放行。
- New 补长度 panic（与空身份同一道构造期闸门，rune 口径）；
- Enqueue 触库前返回哨兵 ErrTaskIDTooLong；
- MaxTaskIDLen/MaxRunnerIDLen 常量派生 Migrate 的 VARCHAR（护栏与
  DDL 同源，改一处忘另一处即复现本缺陷）；
- 测试：recording stub driver 三方言矩阵（panic/哨兵 + driver 零调用
  + 边界放行 + 64 汉字放行 + 真 SQLite 边界对照）。
* fix(sqlqueue): WithMaxAttempts 语义修正为真实执行次数（#86）
claim 与失联复位各消耗一次计数、终态判定只看复位旧值（attempts ≥ n），
实际执行次数是 floor(n/2)+1——WithMaxAttempts(3) 只执行 2 次，API 名字
承诺的「尝试次数」与行为对不上；且副本抖动（失联复位）会把健康任务
提前判进死信。
- 终态判据折算成执行次数：复位时点已执行 k 次 ⟺ attempts=2k-1
  （claim+reset 各 +1 的既有计数口径不变），判 attempts ≥ 2n-1 ⟺
  已执行 ≥ n 次；阈值在 Go 侧算出，不在 SQL 里做除法（MySQL 的 /
  是小数除法，三方言不等价）；
- attempts 表内计数口径不变（queue#70 契约与既有用例不破坏），
  dead_letter 行定格 2n；缺省（n=0）不封顶路径 SQL 逐字不变
  （兼容性红线）；
- 测试：表驱动 n=1..5 断言实际执行次数 1,2,3,4,5 且终态/attempts 定格；
  副本抖动用例（n-1 轮不触顶、恰第 n 轮进终态）；既有 #70 毒任务
  用例按新语义更新轮次。
* fix(sqlqueue): MySQL 索引判存补 table_schema=DATABASE() 限定（#87）
information_schema.statistics 是跨库视图，原判存只绑 table_name +
index_name——同一实例上任意 schema 有同名表 + 同名索引（索引名由表名
派生，dev/prod 并存、mysqldump 复制库、多租户每租户一 schema 都命中）
即误判「已存在」并跳过，本 schema 的索引永不创建且零报错；任务表是
高频读写面，症状表现为吞吐下降与 DB CPU 上涨，排查方向被引向
「任务太多」。
- 判存补 table_schema = DATABASE()（取值绑定不在语句内调用，避开
  个别代理/中间件对 DATABASE() 的支持差异）；
- 索引名改为直接计算，删除从自己刚拼的 ALTER 语句反解的 indexName
  （脆弱且无必要）；
- 测试：stub driver 断言判存语句含 table_schema/DATABASE() 与绑定
  参数面；成对对照（判存命中不发 ALTER / 未命中发两条）；真 SQLite
  重复 Migrate 幂等。
* feat(worker): Pool 补 HeartbeatInterval/StaleAfter 字段与构造期比值闸门（#88）
TaskQueue doc 宣称「周期由宿主经 Pool 字段配置」，但 Pool 没有那两个字段
——心跳周期是包内未导出 var（进程全局、无锁可变、两个 Pool 无法各配一份），
staleAfter 是 const 直接传下去；文档自定的两条红线宿主都遵守不了，失效
静默（配不上不报错，只在心跳轮次错过时以「同一任务双跑」爆出来）。
- Pool 新增 HeartbeatInterval / StaleAfter 字段（0 = 15s/90s 缺省，与
  ClaimTimeout 同一「0 = 缺省」形态），resetStale 归一后传值；
- Start 构造期闸门：HeartbeatInterval >= StaleAfter 直接 panic（与
  NewLeaderElector 对 interval >= ttl 同一道纪律）；置 started 之前拦截，
  修好字段还能再次 Start；
- 缺省比值对齐文档：HeartbeatInterval 20s → 15s（90s/15s = 6× ≥ 文档
  建议的 ≥5×；StaleRunningAfter 保持 90s 不动，argus 在役失联复位延迟
  不变）；
- 收掉进程全局可变的 heartbeatInterval 包级 var（测试改走字段）；
- 测试：配置值逐字透传到 ResetRunningToPending（不是 90s）、心跳周期
  字段驱动循环、缺省必须仍是 90s、成对对照（hb*5 ≤ stale 放行 / 缺省
  放行 / 四种越界 panic）、缺省比值 ≥5× 的守门测试。

- 杂务(deps)：依赖地板追到最新——github.com/oekit/concurrency v0.31.0

| 依赖 | 现地板 | 抬到 |
|---|---|---|
| `github.com/oekit/concurrency` | `v0.30.1` | `v0.31.0` |
## 为什么
矩阵的**发版时刻地板时效**门禁（`verify_tag_floor --ref HEAD`）要求：各仓 HEAD
（= 下次发版会发出去的内容）的依赖地板，相对那一刻的依赖最新版本，不能已落后。
oekit 基座与矩阵模块都在高频发版 ⇒ 地板是**跑步机**，落后一格就会判红。
## 兼容性实测
本工具在提交前跑完 `go build` / `go vet` / `go test -race -count=1 ./...`（全绿才提交）
与 `go mod tidy -diff`（无漂移）⇒ 改动**只影响依赖版本**，零行为差异。
只改 `go.mod` / `go.sum` ⇒ 不需要 CHANGELOG 条目（版本段由下次发版的 `prewrite` 生成）。

- 杂务(deps)：依赖地板追到最新——github.com/oekit/concurrency v0.30.1

| 依赖 | 现地板 | 抬到 |
|---|---|---|
| `github.com/oekit/concurrency` | `v0.29.0` | `v0.30.1` |
| `github.com/oekit/logx` | `v0.8.11` | `v0.8.12` |
## 为什么
矩阵的**发版时刻地板时效**门禁（`verify_tag_floor --ref HEAD`）要求：各仓 HEAD
（= 下次发版会发出去的内容）的依赖地板，相对那一刻的依赖最新版本，不能已落后。
oekit 基座与矩阵模块都在高频发版 ⇒ 地板是**跑步机**，落后一格就会判红。
## 兼容性实测
本工具在提交前跑完 `go build` / `go vet` / `go test -race -count=1 ./...`（全绿才提交）
与 `go mod tidy -diff`（无漂移）⇒ 改动**只影响依赖版本**，零行为差异。
只改 `go.mod` / `go.sum` ⇒ 不需要 CHANGELOG 条目（版本段由下次发版的 `prewrite` 生成）。


## [v0.10.4] — 2026-11-08

- 与 v0.10.3 相比无代码差异（版本号/打包变更）

## [v0.10.3] — 2026-11-08

### 修复(worker,sqlqueue)：构造期闸门补齐（#71/#69）+ 毒任务重试上限与 attempts 观测面（#70）

Merge branch 'main' into fix/audit-issues-71-70-69

- 杂务(deps)：依赖地板追到最新——pkg v0.37.6 / concurrency v0.29.0 / logx v0.8.11（按本仓依赖取齐）

发版时刻地板时效门禁要求追平。concurrency v0.29.0 为纯新增（四原语 feat），无 API 变更。
兼容性实测：go build / go vet / go test -race -count=1 ./... 全绿，go mod tidy -diff 无漂移。


## [v0.10.2] — 2026-11-08

- 杂务(deps)：依赖地板追平 oekit BOM v0.2.9——pkg v0.37.4 / logx v0.8.10 / concurrency v0.28.4

## [v0.10.1] — 2026-11-08

### 修复(worker)：适配 concurrency v0.28.2 PanicHandler 新签名 (ctx, r, stack)——地板同步追平

- oekit/concurrency v0.28.2 是 BREAKING：async.PanicHandler 由
  func(reason string, stack []byte) 改为 func(ctx context.Context, r any, stack []byte)
  （带 ctx 的 spawn 家族把调用方 ctx 透传给 handler，错误上报路径 r 为 fn 返回的 error）
- worker/pool.go 三处站点适配：noopPanicReport / panicRunner / runTask 的 Handler
  换新签名；panic 值由 string 改 any 后原样进结构化日志键 panic=（观测面不变）
- 依赖地板同步追平：concurrency v0.28.2 / logx v0.8.8（matrix-tools#188 发版链）
兼容性实测：go build / go vet / go test -race -count=1 ./... 全绿，go mod tidy -diff 无漂移

- 杂务(deps)：依赖地板追到最新——github.com/oekit/concurrency v0.28.3

oekit/concurrency v0.28.3（审计二轮 bugfix：supervisor 锁内回调死锁/滑窗预占记账/
poll 退避归零/budget 存档拷贝等）落地，地板同步追平（matrix-tools#188 发版链）。
兼容性实测：go build / go vet / go test -race -count=1 ./... 全绿，go mod tidy -diff 无漂移。

- 杂务(deps)：依赖地板追到最新——github.com/oekit/concurrency v0.28.1

| 依赖 | 现地板 | 抬到 |
|---|---|---|
| `github.com/oekit/concurrency` | `v0.28.0` | `v0.28.1` |
## 为什么
矩阵的**发版时刻地板时效**门禁（`verify_tag_floor --ref HEAD`）要求：各仓 HEAD
（= 下次发版会发出去的内容）的依赖地板，相对那一刻的依赖最新版本，不能已落后。
oekit 基座与矩阵模块都在高频发版 ⇒ 地板是**跑步机**，落后一格就会判红。
## 兼容性实测
本工具在提交前跑完 `go build` / `go vet` / `go test -race -count=1 ./...`（全绿才提交）
与 `go mod tidy -diff`（无漂移）⇒ 改动**只影响依赖版本**，零行为差异。
只改 `go.mod` / `go.sum` ⇒ 不需要 CHANGELOG 条目（版本段由下次发版的 `prewrite` 生成）。

- 杂务(build)：补统一 Makefile——本地 ci 门禁与共享 CI 同口径（agentkit#138）

## [v0.10.0] — 2026-11-07

- 杂务(deps)：依赖地板追到最新——github.com/oekit/logx v0.8.6

| 依赖 | 现地板 | 抬到 |
|---|---|---|
| `github.com/oekit/logx` | `v0.8.5` | `v0.8.6` |
## 为什么
矩阵的**发版时刻地板时效**门禁（`verify_tag_floor --ref HEAD`）要求：各仓 HEAD
（= 下次发版会发出去的内容）的依赖地板，相对那一刻的依赖最新版本，不能已落后。
oekit 基座与矩阵模块都在高频发版 ⇒ 地板是**跑步机**，落后一格就会判红。
## 兼容性实测
本工具在提交前跑完 `go build` / `go vet` / `go test -race -count=1 ./...`（全绿才提交）
与 `go mod tidy -diff`（无漂移）⇒ 改动**只影响依赖版本**，零行为差异。
只改 `go.mod` / `go.sum` ⇒ 不需要 CHANGELOG 条目（版本段由下次发版的 `prewrite` 生成）。

- 杂务(logging)：logger 组件名对齐矩阵规则 oagentkit-<module>（agentkit#139）

LeaderElector 默认 logger 组件名 "agentkit-leader" → "oagentkit-leader"，
Pool 默认 logger 组件名 "agentkit-worker" → "oagentkit-worker"，对齐
modules.json loggingRule 要求的 logx.Delegating("oagentkit-<module>") 命名。
行为可见变更：两处默认 logger 的 component 属性值变更，按组件名做日志
过滤/路由的下游（采集规则、告警匹配等）需同步改为新名，否则旧名规则
不再命中；显式传入 Log 字段的调用方不受影响。
Refs oagentkit/agentkit#139

- 杂务(deps)：依赖地板追到最新——github.com/oekit/concurrency v0.28.0

| 依赖 | 现地板 | 抬到 |
|---|---|---|
| `github.com/oekit/concurrency` | `v0.25.1` | `v0.28.0` |
| `github.com/oekit/logx` | `v0.8.3` | `v0.8.5` |
## 为什么
矩阵的**发版时刻地板时效**门禁（`verify_tag_floor --ref HEAD`）要求：各仓 HEAD
（= 下次发版会发出去的内容）的依赖地板，相对那一刻的依赖最新版本，不能已落后。
oekit 基座与矩阵模块都在高频发版 ⇒ 地板是**跑步机**，落后一格就会判红。
## 兼容性实测
本工具在提交前跑完 `go build` / `go vet` / `go test -race -count=1 ./...`（全绿才提交）
与 `go mod tidy -diff`（无漂移）⇒ 改动**只影响依赖版本**，零行为差异。
只改 `go.mod` / `go.sum` ⇒ 不需要 CHANGELOG 条目（版本段由下次发版的 `prewrite` 生成）。
Co-authored-by: Murphy-Yi <zhangyi@Murphy-YideMac-Mini.local>


## [v0.9.0] — 2026-11-07

### 新增(pglease)：fencing token——易主后旧主的写由数据库原子拒绝，不再靠轮询间隔容忍（#39）

## 根因
租约只保证「同一时刻至多一个 winner」，**不保证 winner 知道自己已经输了**：
租约过期被别的副本接管后，旧持有者在下一次 `TryAcquire` 之前一直以为自己是主。
这个窗口是租约语义的固有形态（`worker.LeaderElector` 头注释限定为「≤一个轮询
间隔」），过去唯一的应对是**要求动作幂等**——而这把责任推给了每一个调用方，
且**没有可验证的判据**：动作到底幂等不幂等，只有出事那天才知道。
非幂等动作在窗口里的失效形态很具体：计数被累加两次、通知被重发、外部系统的
状态被旧主的迟到写覆盖。这些都不是「重复执行无害」，而是数据错误。
## 修法
新增**单调 fencing token**，把容忍粒度从「一个轮询间隔」压到「一次写事务」：
- `(*PGLeaseStore).TryAcquireWithToken(ctx, key, holder, ttl) (bool, int64, error)`
  ——与 `TryAcquire` 同一条 UPSERT，多一个 `RETURNING generation`；
- token 语义：首次插入 = 1；**同一 holder 续约保持不变**；**易主 +1**，永不回退；
- `Migrate` 改两步：建表（新库直接带 `generation` 列）+ `ALTER TABLE ... ADD
  COLUMN IF NOT EXISTS generation BIGINT NOT NULL DEFAULT 1`（存量库补列，幂等）；
- `worker.TokenLeaseStore` 是**可选**接口：不给 `LeaseStore` 加方法，避免编译期
  打断所有既有实现（含 `sqllease.Store` 三方言）。需要者用类型断言取能力。
### 为什么「易主才 +1」，不是「每次获取都 +1」
每次 +1 会让持有者在**正常续约**时作废自己刚排队的写（续约与写并发是极常见的
时序），把 fencing 退化成「随机丢写」。而「权没换，token 就不该换」恰好就是它
要表达的语义。
### 为什么是「一条语句」而不是「先查再写」
`RETURNING` + `ON CONFLICT` 让「是否抢到」与「拿到哪个 token」成为**同一次原子
操作**的产物。拆两条语句时，两者之间可以插进另一个写者——调用方拿到的是**别人
的** token，却以为自己持有它。那比没有 token 更坏：它给了一个看起来可信的 CAS
依据。
### 消费形态取 `generation <= $token`，不是 `=`
业务行上存「最后一次被接受的写的 token」。`=` 只适用于「每个任期重建的业务行」，
真实表是长期存活的，新主接手时行上还是旧 token ⇒ `=` 会把**新主的首写一并拒掉**
（真库实测 0 行，见下反向验证第 6 条）。`<=` 让同一持有者反复写、严格更旧的
token 恒被拒；代价是「新主接管后首写之前」旧主的写仍可能落地，由「接管后立即
写一次」关闭。
## 兼容性
`TryAcquire` / `Release` / `Cleanup` / `LeaseStore` **逐字未变**，既有调用方零改动。
新增能力全部走新符号（新方法 + 新可选接口 + 新列）。
## 反向验证（6 条，均实测红）
| 注入 | 报错 |
|---|---|
| 易主支不 `+1`（token 永不变化，CAS 恒成立） | `易主支必须 +1，否则 token 从不变化、CAS 永远成立` |
| 续约支也 `+1`（作废自己在飞的写） | `续约支不得 +1——那会让持有者作废自己刚排队的写` |
| token 拆成两条语句（先 `SELECT` 再 UPSERT） | `获取与取 token 必须是同一条语句，实得 2 条` |
| 去掉触库前校验（空 key/holder 发到库里） | `err = <nil>, want pglease: key 不能为空` |
| `Migrate` 只建表不补列（存量库补不上列） | `Migrate 应发两条语句（建表 + 补列），实得 1 条` |
| 消费形态退回 `generation = $token`（真库） | `新 token 2 应写得进去，实得 0 行` |
前五条在 `pglease/fencing_token_test.go`（stub driver，钉语句形态与分支结构），
第六条在 `pglease/pglease_integration_test.go`（DSN 门控，真库）。
### 真库用例是成对的
`TestIntegrationFencingTokenMonotonic` 先让持权者用**当前** token 写成功
（control），再在易主后断言同一调用被拒（treatment）——只看「0 行」无法区分
「fencing 生效」与「CAS 形态本身就不成立」。
## 落地范围
`pglease` 是唯一随仓提供的 `LeaseStore` 实现（另有 `sqllease` 三方言，本次未接入
token——它的表结构不归本版管）。宿主若要 fencing，实现 `TokenLeaseStore` 即可。

## [v0.8.0] — 2026-11-07

### 修复(pglease)：补 Cleanup 清理过期租约行——崩溃残留的行不再永久占着主键（#33）

## 根因
租约行原本只有两种离开方式里的**一种**：持有者显式 `Release`（优雅停机）。
进程崩溃/被 kill 留下的行再没有任何路径被删除——`key` 是主键，行永久占位。
动态 key 场景（临时租约、按任务/租户拼 key）下表只增不减，查询与索引体积
一起退化，且**零信号**：功能全对，只是越来越慢。
`TryAcquire` 的 `ON CONFLICT` 子句带 `expires_at < now()`，过期行照样可被夺
——所以这不是安全修复，压的是运维面。
## 修法
新增 `(*PGLeaseStore).Cleanup(ctx) (int64, error)`：`DELETE FROM <table>
WHERE expires_at < now()`，返回删除行数。
两条口径刻意对齐既有实现：
- **DB 时钟**（`now()` 而非参数化 `time.Now()`）：清理判据与过期判据必须是同一个
  钟。应用时钟偏慢的副本会把**还没过期**的行删掉，等于替对端提前让位，双主窗口
  凭空打开——与 `TryAcquire` 用 DB 时钟同一条理由。
- **不做「顺带清理同 key 过期行」**：那条路径只清得到本次竞争的那一个 key，高
  churn 下绝大多数 key 再也不会有第二次竞争，过期行永远等不到它的「顺带」——
  全局扫描是这类表唯一能收敛的形态。
## 反向验证（三条，均实测红）
| 注入 | 报错 |
|---|---|
| 去掉 `WHERE`（无条件 DELETE） | `过期判据必须是 expires_at < now()` 与 `清理语句必须带 WHERE` 两条同时红 |
| 不透传 `RowsAffected`（恒返 0） | `删除行数 = 0, want 7——RowsAffected 未透传` |
| 改用应用时钟 `expires_at < $1` | `实得 "... WHERE expires_at < $1"` ✓ 红（与 TryAcquire 不同钟） |
## 用例
`pglease/cleanup_test.go`（stub `database/sql/driver`，零新依赖）：
- `TestCleanupDeletesExpiredRows`：判据 `expires_at < now()`、指向配置表、无参数、
  行数透传四条；
- `TestCleanupKeepsUnexpiredRows`：成对对照——**只测「有 DELETE 语句」是测不出
  判据方向的**，`DELETE FROM t`（无 WHERE）同样有语句，因此把「必须带 WHERE」
  单独钉住。
`pglease/pglease_integration_test.go` 新增 DSN 门控用例
`TestIntegrationCleanupRemovesExpiredOnly`：真 PG 上造一行过期 + 一行活着，
断言只删 1 行、活行仍可续约、过期行清理后空闲可取。**已在本机 PostgreSQL 实跑
通过**（`AGENTKIT_PG_TEST_DSN=… go test ./pglease -run Integration`）。
* fix(pglease): cleanup_test 删未使用的 record 方法（lint unused）

### 修复(worker)：角色翻转回调改异步调度——慢回调不再顶住续约，双主窗口回到「≤一个轮询间隔」（#41）

## 根因
`onGained` / `onLost` 在 `setLeader` 内**同步**执行，而 `setLeader` 由 `tick`
调用——也就是说，回调跑在竞选 goroutine 的续约节拍里。一个阻塞回调
（等外部服务、做重活）会把下一次 tick 整个顶后：租约在回调执行期间过期，
其他副本接管，而本实例的 `IsLeader()` 仍为 true。
双主窗口由此从文档承诺的「≤ 一个轮询间隔」扩大成「**回调耗时 + 一个轮询间隔**」，
且没有任何报错——两个副本各自安好。
## 修法
回调与竞选循环解耦：`setLeader` 只入队，专用调度 goroutine 按 FIFO 执行。
- `cbCh chan func()`（容量 64）+ 调度 goroutine，`Start` 里拉起；
- 竞选 goroutine 是**唯一写者**（`setLeader` 只从 `tick` 里调），
  故退出时 `close(cbCh)` 与入队不会撞上；
- 反压方向刻意选「丢通知」而非「阻塞入队」：回调卡死时阻塞入队等于把回调的
  故障重新传染给续约，正是这条要拆掉的耦合。丢的时候 `Warn` 留痕；
- 单个回调的 panic 在 `runCallback` 内收编——调度器是唯一执行者，它死了就
  再也不会有任何角色翻转通知（`Warn` 留痕）。
### 顺带把 `done` 的含义补全
`done` 原本只表示「竞选 goroutine 退出」。改异步之后若不等待，`Stop` 返回时
在飞回调可能还没跑完，调用方读到的组件状态仍是「还没收到 onGained」——而它
指望 `Stop` 保证的正是这件事。
故退出路径改为 `finishCampaign`：先 `close(cbCh)`（封住新回调），再等
`cbDone`（排空），最后 `close(done)`。等待有界（`callbackDrainTimeout = 5s`），
超时 `Warn` 并照常关 `done`——回调是通知不是停机屏障，卡死的回调不能把
`Stop` 拖成无界等待。
### `started` → `everStarted`
`Stop` 的 no-op 判据原本读 `started`，而它在竞选 goroutine 退出时被置回 false。
加上「等回调排空」之后，这个窗口会让 `Stop` 提前返回、跳过排空。改用
`everStarted`（`Start` 里置位、永不重置）：no-op 只发生在「从未 Start」，
一旦 Start 过，`Stop` 必定等到 `done`。
## 用例
新增 `worker/leader_callback_test.go`：
1. **`TestSlowCallbackDoesNotStallRenewal`**（判别式）——把 `onGained` 卡住
   3 个完整租期，期间让别的持有者 `B` 去抢同一把锁：续约还在跑则抢不到，
   续约被回调顶住则抢得到。另断言 `IsLeader()` 未翻 false。
2. **`TestStopWaitsForCallbacksToDrain`**——`Stop` 返回后在飞回调必须已完成
   （钉「改异步别把等待一起丢掉」；同步实现下它同样成立，故不是判别式）。
3. **`TestCallbackPanicDoesNotKillDispatcher`**——`onGained` 故意 panic，
   随后让存储抖动使本实例失主，断言 `onLost` 照常送达（调度器还活着）。
既有 `TestLeaderElectorGainedAndHandover` / `TestLeaderElectorStopWithInflightTick`
的计数器改 `atomic.Int32`——回调现在由另一个 goroutine 执行，非原子读会被
`-race` 抓到。
## 反向验证
**（一）回调改回同步** → 判别式用例红：
```
leader_callback_test.go:57: 回调阻塞期间租约被 B 抢走——续约被回调顶住了（queue#41）
```
**（二）去掉 `runCallback` 的 panic 收编** → 第三条红（调度器被打死）：
```
leader_callback_test.go:144: 超时：失主后 onLost 未送达——调度器已被上一次回调的 panic 打死
```
## 验证
`go test ./...` 全绿；`CGO_ENABLED=1 go test -race ./worker/` 绿。
关联 oagentkit/queue#41

### 修复(pglease)：TryAcquire/Release 校验 key/holder 不超过列宽——超长值不再以 DB 通用错误呈现为「存储故障」（#34）

* fix(worker): LeaderElector.Stop 未启动时不再烧掉生命周期——Start 之前误调一次不再静默砖化（#37）
## 根因
`worker/leader.go` 的 `Stop` 无条件 `close(e.stop)`，然后才看 `started`：
```go
e.stopOnce.Do(func() { close(e.stop) })
if e.started.Load() { <-e.done }
```
于是「Start 之前误调一次 Stop」把 **startOnce 与 stop 一起烧掉**。之后
`Start` 的竞选 goroutine 首个 `tick` 就 `stopping()==true` 直接退出——
选主器**永不参选**、`IsLeader()` 恒 false、**无日志无错误**。误调用一次
即静默砖化。
这与同包 `Pool.Stop` 的语义相反（`pool.go` 注释写明「未启动时调用为 no-op，
不消耗停机状态——Stop 在 Start 之前误调用后，真正的 Stop 依然有效」）。
## 修法
`Stop` 在**同一把锁内**判断 `started`，未启动直接返回、不关 `stop`：
```go
e.mu.Lock()
started := e.started.Load()
if !started {
	e.mu.Unlock()
	return
}
e.stopOnce.Do(func() { close(e.stop) })
e.mu.Unlock()
<-e.done
```
`Start` 里 `started.Store(true)` 也挪进同一把锁。**这一步不是顺带的美化**：
若只改 `Stop` 而不锁 `Start`，就存在「Stop 读到 `started==false` 当成未启动
no-op，`Start` 紧接着照常参选」的窗口——调用方以为已停机，选主器还在跑，
比原来的缺陷更坏（原来是「该跑的没跑」，那个窗口是「以为停了还在跑」）。
锁只包住「读状态 + 关 stop」，`<-e.done` 在锁外等——否则慢速存储层的
accquire 会让并发的第二个 Stop 卡在锁上。
## 用例
新增 `worker/leader_lifecycle_test.go`：
1. **`TestLeaderElectorStartAfterStopBeforeStart`**——`Stop()`（从未 Start）
   → `Start()` → 等 `onGained`（事件驱动，不靠 sleep 猜首次 tick）
   → 断言 `IsLeader()` → `Stop()` → 断言 `IsLeader()==false` 且存储中
   租约已让出。覆盖「不砖化」与「之后真正的 Stop 依然有效」两半。
2. **`TestLeaderElectorStopIdempotentAfterStart`**——Start 之后的重复 Stop
   仍幂等：已停机后再调不挂死在 `<-e.done`。
## 反向验证
退回无条件 `close(e.stop)`：
```
--- FAIL: TestLeaderElectorStartAfterStopBeforeStart (2.00s)
    leader_lifecycle_test.go:39: Stop 先于 Start 之后，Start 未能参选成功
        ——选主器被静默砖化（queue#37）
```
第二条用例在旧实现下仍绿——这是刻意的：它钉的是「别为了修 #37 把幂等改坏」，
正反两种实现都该过；只有第一条是判别式的。
## 验证
`go test ./...` 全绿；`CGO_ENABLED=1 go test -race ./worker/` 绿。
关联 oagentkit/queue#37
* fix(pglease): TryAcquire/Release 校验 key/holder 不超过列宽——超长值不再以 DB 通用错误呈现为「存储故障」（#34）
## 根因
`pglease` 的 `Migrate` 建的是 `key VARCHAR(64)` / `holder VARCHAR(128)`，但
`TryAcquire` / `Release` 的 Go 侧只校验「非空」（`validateIdentity`），不校验长度。
超长值一路走到 Postgres，报的是
```
value too long for type character varying(64)
```
调用方拿到的是 driver 通用错误，**读不出哪个参数超限、上限是多少**。一个配置
写错（key 拼了长前缀、holder 塞了整条连接 URI）表现为「存储故障」——排查方向
从第一步就是错的。
## 修法
`validateIdentity` 补两条长度判据，位置与空值校验同级（触库之前）：
```go
if n := utf8.RuneCountInString(key); n > MaxKeyLen { ... }
if n := utf8.RuneCountInString(holder); n > MaxHolderLen { ... }
```
新增 `MaxKeyLen = 64` / `MaxHolderLen = 128` 常量，与 DDL 逐字对应；
哨兵 `ErrKeyTooLong` / `ErrHolderTooLong` 让调用方能 `errors.Is` 区分
「配置问题」与「存储故障」，并在错误文本里带上实得字符数。
### 判据为什么是 rune 数而不是 `len()`
PostgreSQL 的 `VARCHAR(n)` 按**字符**计，不是字节。逐字节判会把 64 个汉字
（64 字符 / 192 字节）判成超限——那是把**合法输入拦在本地**，比不校验更难查：
调用方看到「key 超长」却发现自己的 key 明明只有 64 个字。这条选择在用例里
是判别式的（见下）。
## 用例
新增 `pglease/length_guard_test.go`，沿用本包既有手法（连不上的 DSN ⇒
校验在位则触库前返回哨兵，漏校验才报连接错误，既不需要真库又能区分
「报错」与「报对了错」）：
1. **`TestOversizeIdentityRejectedBeforeDB`**——超长 key / 超长 holder ×
   `TryAcquire` / `Release` 四个子用例。Release 同样要验：超长 holder 的
   DELETE 匹配不到自己那把锁，让位会静默失败。
2. **`TestLengthGuardCountsRunesNotBytes`**——64 个汉字（192 字节）必须放行、
   65 个汉字必须拒。这是「rune 口径」的判别点：字节口径下第一条子用例红。
3. **`TestBoundaryLengthsPassGuard`**——恰好等于上限的 key/holder 不得被拒
   （成对对照，防「一律拒绝」也能让上面绿）。
## 反向验证
两轮，分别对应两种错实现：
**（一）去掉长度校验** → 四个子用例全红，报的正是跨库前的连接错误：
```
length_guard_test.go:51: 应返回 ErrKeyTooLong，得到 ok=false err=dial tcp 127.0.0.1:1: connect: connection refused
```
**（二）改成 `len()` 字节口径** → rune 用例红：
```
length_guard_test.go:98: 64 个汉字（192 字节）是 64 字符，不该被当成超长:
    pglease: key 超长（上限 64 字符）: 实得 192 字符
```
## 验证
`go test ./...` 全绿。
关联 oagentkit/queue#34

### 修复(worker)：LeaderElector.Stop 未启动时不再烧掉生命周期——Start 之前误调一次不再静默砖化（#37）

## 根因
`worker/leader.go` 的 `Stop` 无条件 `close(e.stop)`，然后才看 `started`：
```go
e.stopOnce.Do(func() { close(e.stop) })
if e.started.Load() { <-e.done }
```
于是「Start 之前误调一次 Stop」把 **startOnce 与 stop 一起烧掉**。之后
`Start` 的竞选 goroutine 首个 `tick` 就 `stopping()==true` 直接退出——
选主器**永不参选**、`IsLeader()` 恒 false、**无日志无错误**。误调用一次
即静默砖化。
这与同包 `Pool.Stop` 的语义相反（`pool.go` 注释写明「未启动时调用为 no-op，
不消耗停机状态——Stop 在 Start 之前误调用后，真正的 Stop 依然有效」）。
## 修法
`Stop` 在**同一把锁内**判断 `started`，未启动直接返回、不关 `stop`：
```go
e.mu.Lock()
started := e.started.Load()
if !started {
	e.mu.Unlock()
	return
}
e.stopOnce.Do(func() { close(e.stop) })
e.mu.Unlock()
<-e.done
```
`Start` 里 `started.Store(true)` 也挪进同一把锁。**这一步不是顺带的美化**：
若只改 `Stop` 而不锁 `Start`，就存在「Stop 读到 `started==false` 当成未启动
no-op，`Start` 紧接着照常参选」的窗口——调用方以为已停机，选主器还在跑，
比原来的缺陷更坏（原来是「该跑的没跑」，那个窗口是「以为停了还在跑」）。
锁只包住「读状态 + 关 stop」，`<-e.done` 在锁外等——否则慢速存储层的
accquire 会让并发的第二个 Stop 卡在锁上。
## 用例
新增 `worker/leader_lifecycle_test.go`：
1. **`TestLeaderElectorStartAfterStopBeforeStart`**——`Stop()`（从未 Start）
   → `Start()` → 等 `onGained`（事件驱动，不靠 sleep 猜首次 tick）
   → 断言 `IsLeader()` → `Stop()` → 断言 `IsLeader()==false` 且存储中
   租约已让出。覆盖「不砖化」与「之后真正的 Stop 依然有效」两半。
2. **`TestLeaderElectorStopIdempotentAfterStart`**——Start 之后的重复 Stop
   仍幂等：已停机后再调不挂死在 `<-e.done`。
## 反向验证
退回无条件 `close(e.stop)`：
```
--- FAIL: TestLeaderElectorStartAfterStopBeforeStart (2.00s)
    leader_lifecycle_test.go:39: Stop 先于 Start 之后，Start 未能参选成功
        ——选主器被静默砖化（queue#37）
```
第二条用例在旧实现下仍绿——这是刻意的：它钉的是「别为了修 #37 把幂等改坏」，
正反两种实现都该过；只有第一条是判别式的。
## 验证
`go test ./...` 全绿；`CGO_ENABLED=1 go test -race ./worker/` 绿。
关联 oagentkit/queue#37

### 修复(worker)：NewLeaderElector 要求 interval 严格小于 ttl——续约节拍不短于租期即双主窗口（#36）

构造期只拦了 `ttl<=0` / `interval<=0` / 空身份，**没拦两者的关系**。而关系才是
双主窗口的成因：
ticker 每 `interval` 续一次，租约活 `ttl`。`interval >= ttl` 时，租约在两次
续约之间**必然**过期——下一次 `TryAcquire` 之前别的副本就能拿走，而本实例的
`IsLeader()` 仍是 `true`。两边同时自认 leader，日志各自安好，**零报错**。
放到构造期拦是唯一办法：窗口出现在「换主的那一瞬」，运行时事后根本无法区分
「正常换主」与「双主」。这与既有的 `ttl<=0 / interval<=0` 是同一类闸门，只是
判据从「单项是否合法」变成「两项的关系是否留出余量」。
判据取**严格小于**（`interval < ttl`），相等也算非法：ticker 到位的那一刻租约
刚好到期，续约与过期在同一时刻竞争，等价于没有余量。
校验位置放在身份校验（key/holder）之后、构造之前——ttl/interval 的非正校验在
最前，因为那是「单值非法」，比「关系非法」更基础。
## 反向验证
去掉 `if interval >= ttl` 这一块：
```
--- FAIL: TestTTLIntervalRelationBoundary
    interval==ttl 应构造期 panic（ttl=1s interval=1s）
    interval>ttl 应构造期 panic（ttl=100ms interval=1s）
```
两种形态各自 red（用 `t.Errorf` 而非 `Fatalf`，免得前者挡住后者——只报一条
的用例分不清「两条都拦住了」还是「只拦了第一条」）。恢复后绿。
## 用例
- `TestNewLeaderElectorRejectsInvalidParams` 补两行：`interval==ttl`、
  `interval>ttl`，与既有的 `ttl<=0` / `interval<=0` / 空 key / 空 holder 同表。
- 新增 `TestTTLIntervalRelationBoundary`：把关系判据的**边界**单独钉一次。
  - 相等与更长都是非法（两形态独立断言）；
  - **合法侧不误伤**：`ttl=1s, interval=999ms` 构造成功并 Start/Stop 正常
    ——否则「一律 panic」也能让上面几条绿，而选举整个不可用。
既有用例的参数（ttl/interval ≥ 4）都在新判据的合法侧，未被波及。
`go vet` + `go test -race -count=1 ./...` 绿。

### 修复(sqllease)：按方言引用标识符——MySQL 分支的 `key` 保留字不再让三条语句全成语法错误（#32）

`sqllease` 的所有语句都把列名裸写：
```sql
CREATE TABLE IF NOT EXISTS agentkit_lease (key VARCHAR(64) PRIMARY KEY, …)
UPDATE agentkit_lease SET holder = ?, expires_at = … WHERE key = ? AND (…)
DELETE FROM agentkit_lease WHERE key = ? AND holder = ?
```
`key` 是 **MySQL 保留字**，裸用即语法错误 ⇒ MySQL 分支的建表、取租约、放租约
**全部不可用**。而这个缺陷一路静默：三方言测试矩阵只覆盖 SQLite，SQLite 不把
`key` 当保留字，裸用照样跑——同一份 SQL 在测试里绿、在生产方言上不可执行。
新增 `Store.ident(name)` 按方言引用标识符：MySQL 用反引号（必需），
Postgres/SQLite 用标准双引号（SQLite 两种都接受，标准形态优先）。四处全部走它：
建表、条件 UPDATE、INSERT、DELETE，以及 `nowCmp()` 里的 `expires_at`。
## 反向验证
两处独立回退：
1. 建表与条件 UPDATE 一起退回裸 `key`——
   ```
   --- FAIL: TestMySQLStatementsQuoteReservedKey
       key 未被引用（MySQL 保留字/一致性）：
       CREATE TABLE IF NOT EXISTS `agentkit_lease` (
         key VARCHAR(64) PRIMARY KEY, …
   --- FAIL: TestMySQLMigrateDDLIsBackticked
   ```
2. **只**把条件 UPDATE 的 `key` 退回裸用（证明 UPDATE 不是靠建表那条顺带过的）——
   ```
   mysql_quoting_test.go:102: key 未被引用（MySQL 保留字/一致性）：
       UPDATE `agentkit_lease` SET `holder` = ?, `expires_at` = DATE_ADD(NOW(), INTERVAL ? SECOND)
       WHERE key = ? AND (`holder` = ? OR `expires_at` < NOW())
   ```
两条都红过才恢复。
## 用例：在驱动层取证，不需要 MySQL 服务器
新增 `sqllease/mysql_quoting_test.go` 三条，用一个记录 SQL 的假驱动
（`database/sql/driver` 接缝）——MySQL 分支没有真库可连，但「发出去的语句长
什么样」在驱动层就能看到，这正是语法正确性的直接证据：
- `TestMySQLStatementsQuoteReservedKey`：跑 Migrate + TryAcquire + Release，
  对**每条**语句断言 `key` / `holder` / `expires_at` 都无裸出现，且含 `` `key` ``。
- `TestMySQLMigrateDDLIsBackticked`：建表语句单独钉一次（最容易被漏改的一条）。
- `TestPostgresUsesStdQuotes`：**成对对照**——Postgres 走双引号且不得出现反引号
  （反引号在 PG 里是语法错误），防止「一律反引号」把另一个方言改坏。
SQLite 分支由既有真库用例继续覆盖（内存库跑全套语义）。
`go vet` + `go test -race -count=1 ./...` 绿。

### 修复(lease)：TryAcquire 拒绝非正 ttl——租约不再「落库即过期」而选举还自称成功（#35）

`expires_at` 由 ttl 直接算出（pg: `now() + make_interval(secs => $3)`；
sql: 同口径）。ttl ≤ 0 时租约**落库那一刻就已过期**：
- 下一次 `TryAcquire` 立刻判「已过期」，任何副本都能抢走；
- 而「同一时刻至多一个 leader」正是这个包存在的理由 —— 非独占由此被击穿；
- **失效是全静默的**：每次选举都「获取成功」，只是从来不是独占。零报错、
  零日志、测试全绿，只有并发一起跑才看得出双头。
配置漏填（JSON/env 零值）是最常见的触发面——那种情况下「ttl 必须 > 0」
远比在库里看到一堆瞬间过期的行有用。
两个实现同病同治，各自加 `ErrInvalidTTL` 哨兵 + `validateTTL`：
- `pglease.PGLeaseStore.TryAcquire`
- `sqllease.Store.TryAcquire`
校验位置在 `validateIdentity` 之后、**任何 SQL 之前**——与 queue#43 的空身份
校验同一条纪律：判据必须发生在「条件命中之前」，否则脏输入已经进了库。
## 反向验证
把两处 `validateTTL` 调用与函数体一并删掉：
```
--- FAIL: TestNonPositiveTTLRejected
    ttl=0s 应返回 ErrInvalidTTL，得到 ok=true err=<nil>
--- FAIL: TestNonPositiveTTLRejectedBeforeDB
    ttl=0s 应返回 ErrInvalidTTL，得到 ok=false err=dial tcp 127.0.0.1:1: connect: connection refused——走到 SQL 即落库即过期
```
第一条整个就是缺陷本体：`ok=true` —— **零 ttl 的租约被当成获取成功**。
第二条同时说明校验位置（没校验就会去拨号）。恢复后绿。
## 用例
- `sqllease`（真 SQLite，内存库）：
  - `TestNonPositiveTTLRejected`：`0 / -1s / -1h` 三种都返回 `ErrInvalidTTL`
    且 `ok=false`；额外断言**库里一行都没写**（校验必须在写库之前）。
  - `TestNonPositiveTTLDoesNotBreakPositive`：成对对照——正 ttl 仍获取成功、
    他人持有仍被拒（否则「一律拒绝」也能让上一条绿而选举整个不可用）。
- `pglease`（连不上的 DSN，无需真库）：
  - `TestNonPositiveTTLRejectedBeforeDB`：三种非正 ttl 都必须返回哨兵错误
    ——漏校验时报的是连接错误，断言因此能区分「报错」与「报对了错」。
  - `TestPositiveTTLStillPassesGuard`：正 ttl 不当成 `ErrInvalidTTL`。
`go vet` + `go test -race -count=1 ./...` 绿。

### 修复(worker)：停机两处兜底超时不再静默——worker/心跳未退出必须留痕（#40）

`Stop` 里两处 `case <-time.After(10 * time.Second):` 分支体是空的（第二处只有
一行注释）。失效形态是最坏的那种：worker 卡在不理会 ctx 的 `Run` 里，`Stop`
照样置 `stopped=true` 并打 Info「stopped」——运维读到的是**干净停机**，
而 goroutine 与任务都还在跑，run 结束后也没人知道。同文件其余超时都带 Warn，
这两处是纪律缺口。
- 两处兜底超时抽成 `hardExitTimeout`（10s），并各补一条 Warn：
  - `agentkit.worker.stop_exit_timeout`：带 `stuck_workers`（**真数**，不是占位）
    + `workers` + `waited`，action 写明「本 Pool 已标记 stopped，这些 goroutine 仍在运行」；
  - `agentkit.worker.stop_heartbeat_timeout`：心跳循环未退出，同形缺陷一并修
    （它就是同一函数里的第三段等待）。
- `liveWorkers int32`（atomic）记录未退出的 worker 数——「还有几个没退出」是这条
  Warn 唯一能给出的硬信息，只报 `N` 会把「1 个卡住」说成「N 个卡住」。
- 状态位**仍**置 `stopped`：Pool 是单次生命周期，`stopOnce` 已消耗，泄漏的
  goroutine 没有救回来的路径——所以留痕是唯一诚实的做法，而不是靠状态位
  假装还有救（`stopped=false` 反而会让后续 `Start` 造出第二批 worker）。
  这个取舍写进了代码注释。
- `hardExitTimeout`：const 的话每个用例固定烧 10s；
- `heartbeatInterval = HeartbeatInterval`：心跳首跑在首个周期之后（20s），
  不压到毫秒级，那条心跳 Warn **永远验不到**——一个从没被触发过的分支
  等于没写。两者都写明「除测试外无人改它」。
| 注入 | 结果 |
|---|---|
| `stop_exit_timeout` 退回空分支 | `worker 未退出必须打 Warn 留痕，实际日志: [started claimed stop_grace_exceeded stopped]` —— **与 issue 描述的「读到的是干净停机」逐字吻合** |
| `stop_heartbeat_timeout` 退回注释分支 | `实际日志: [started stopped]` |
三对钉：
- `TestStopWarnsWhenWorkerIgnoresContext`：`Run` 刻意不 `select ctx.Done()`，
  等到它真在跑再 `Stop(20ms)`；断言 Warn 级别 + `stuck_workers == 1`；
  放开后轮询 `liveWorkers == 0`，证明「未退出」这个判定当时是真准的（不是猜的）。
- `TestStopWarnsWhenHeartbeatRoundStuck`：队列的 `TouchRunningHeartbeats` 阻塞，
  用 `entered` 闸确认轮次真在飞，再 `Stop`。
- `TestStopCleanDrainDoesNotWarn`：**成对的反向对照**——正常排空不得打这两条
  Warn，且必须有 `stopped` Info。只有「开着绿」的用例等于没钉住：若 Warn 无条件
  打，前两条照样过。
`go vet` + `go test -race -count=1 ./...` 四包全绿。

### 修复(worker,sqlqueue)：心跳改按在飞任务集续期——panic/报错任务不再被自家心跳续活（#42）

根因不是「少写一行清理」，而是盲续本身两难：
- runTask 的 panic/错误分支只落日志，任务行停在 status='running' +
  runner_id=本实例（Pool 不写终态，那是宿主职责）；
- 只要本进程还活着（继续领别的任务、心跳继续跑），
  TouchRunningHeartbeats 按 runner_id 盲续就会把这些行的心跳一直续成新鲜的；
- 于是 ResetRunningToPending(90s) 永不命中——pool.go 里宣称的
  「任务由 StaleRunningAfter 心跳过期机制复位重跑」这条恢复路径，
  对「同进程内失败的任务」**结构性失效**：任务永久卡 running、
  无人标记终态、attempts 双增量也没有消费方（静默丢失）。
盲续把两件事混成了一件：「我在跑它」（活信号）与「它归我」（归属）。
修复就是把它们分开——续期集只含本进程真正在执行的任务。
- worker 新增可选能力接口 ScopedHeartbeater（TouchHeartbeatsFor(ctx, taskIDs)）。
  用类型断言探测而不往 TaskQueue 里加方法：实现方在仓外（宿主自实现的
  GORM/Redis 队列），加方法 = 破坏性变更，现有实现当场编译不过。
- Pool 用 inflight map（+ 独立 mutex）记在飞集；loop 的 claim 成功后走
  runTracked（登记 → 执行 → defer 注销），三条收尾路径都注销。
- heartbeat：实现 ScopedHeartbeater 时取快照续期，**空集直接不发语句**；
  否则退回盲续（未实现者行为一字不变）。
- sqlqueue 实现 TouchHeartbeatsFor：单语句 UPDATE，三重守卫
  runner_id + status='running' + task_id IN (...)。空集返回 nil——
  绝不能退化成「无过滤条件」，那正是要修的盲续。
- TaskQueue / runTask 的 godoc 把恢复契约的前提写明（「只在
  ScopedHeartbeater 时成立」），不再无条件宣称。
反向验证（四处，各自只打掉自家护栏，其余不动）：
- 心跳退回盲续 ⇒ TestHeartbeatRenewsOnlyInflight / TestHeartbeatSkipsEmptySet /
  TestPoolHeartbeatDuringRunAndAfter 三红
- 去掉 task_id 守卫（IN (...) 恒真）⇒ reset=0，**与 issue 复现逐字一致**
  （「ResetRunningToPending(90s) reset rows = 0」）
- 去掉 trackDone ⇒ Run 返回后任务仍在续期集，panic 用例红（热路径）
- 空集退化成盲续 ⇒ worker 与 sqlqueue 两侧的空集用例各红一条
用例取舍：heartbeat 周期是常量（20s）不可注入，故不靠等真实节拍——
直接调 p.heartbeat() 并断言续期集内容（纯函数化），端到端那条用阻塞 Run
把任务钉在飞状态、事件驱动等待注销落定。sqlqueue 侧用 SQLite 内存库 +
把 heartbeat_at 回拨 3600s（与既有 TestQueueLifecycle 同款手法）代替真等 90s。

### 修复(sqllease,pglease,worker)：空 key/holder 在库层与构造期双双 fail-loud（#43）

根因：租约的「本人持有则顺延」是**内容比对**而不是身份比对——
sqllease 的 `WHERE key=? AND (holder=? OR expires_at<now)`、
pglease 的 `ON CONFLICT ... WHERE holder = EXCLUDED.holder`。
空 holder 让这个条件对**所有**空 holder 副本恒真：A 建租约（holder=''）、
B 续约时 holder='' 匹配到 A 那行 ⇒ 两副本同时自认 leader，
而「同一时刻至多一个 leader」这条红线**零报错**破产。
这正是 #27/#29 同族「构造期 fail-fast」的漏网项：NewLeaderElector 校验了
ttl/interval/key，独缺 holder——而 holder 与 key 是一对身份（key 指哪把锁、
holder 指谁在持），空值必是装配错误。
两层修，缺一不可：
- 构造期：NewLeaderElector 对 holder=="" panic（与 key 同款）。
  只能拦按规矩走它的调用方。
- 库层：sqllease.Store 与 pglease.PGLeaseStore 的 TryAcquire/Release
  对空 key/holder 返回 ErrEmptyKey/ErrEmptyHolder，**在拼 SQL 之前**拦住——
  直接用 LeaseStore 接口、或绕过选主器自己拼租约的调用方没有构造期那道闸。
  Release 同样要拦：空 holder 的 DELETE 会删掉 `holder = ''` 那把锁，
  也就是它认不出的那把。
- worker.LeaseStore 的接口 godoc 把这条写进契约（含「为什么不能用内容比对
  来表达身份」），自定义实现方也能看到。
反向验证（三处，各自只打掉自家护栏，其余不动）：
- 去掉构造期 holder 校验 ⇒ TestNewLeaderElectorRejectsInvalidParams 红（"empty holder: 应构造期 panic"）
- 去掉 sqllease 的 validateIdentity ⇒ 得 ok=true err=<nil>，**双主被复现**
- 去掉 pglease 的 validateIdentity ⇒ 得到连接错误而非 ErrEmptyHolder
  （这正是「只断言 err != nil」抓不到的情形：报了个别的错也算绿）
用例取舍说明：pglease 的用例给的是**连不上**的 DSN（127.0.0.1:1），
校验在位时在建连之前返回，漏掉时返回连接错误 ⇒ 断言 `errors.Is(err, ErrEmptyHolder)`
既能区分「报错」与「报对了错」，又不需要真库。

- 杂务(deps)：依赖地板追到最新——concurrency v0.27.0 / logx v0.8.4

## [v0.7.0] — 2026-11-06

### 新增(sqllease,sqlqueue)：SQL 参考实现三方言 + LeaseStore/TaskQueue 契约硬化

* feat(sqllease,sqlqueue): SQL 参考实现三方言 + LeaseStore/TaskQueue 契约硬化（#24 #25 #26）
queue#26——时钟语义契约 + 参考实现进默认路径：
- worker.LeaseStore doc 补 ⚠️ 时钟语义红线：expires 写入与过期判定必须同钟
  （DB 时钟），应用时钟漂移等量放大/缩短租约时长的隐蔽失效形态写明
  （pglease v0.10.29 修复，下游按旧形态复制的 LeaseStore 会静默丢掉）
- worker re-export pglease（PGLeaseStore 别名 + NewPGLeaseStore）：「实现
  接口」不再是默认路径；pglease 测试改外部包断言接口（避免成环）
queue#24——sqllease 参考实现（database/sql，PostgreSQL/MySQL/SQLite）：
- 判-占-续单语句条件 UPDATE（无查后写竞窗）+ DB 时钟 + INSERT 冲突降噪
  （竞态抢先 INSERT 是预期路径，错误不外露，静默回退条件 UPDATE——argus
  双副本 duplicate-key 红字刷屏的降噪诉求）
- 五语义点 + 并发单 winner（-race 16 goroutine）+ 冷启动并发首抢收敛钉子
queue#25——sqlqueue 参考实现 + TaskQueue 时序约束契约：
- 候选 SELECT 只是提示，状态迁移在单语句守卫 UPDATE（WHERE status='pending'）
  里完成——并发双抢恰一 winner（-race 钉子）；多任务并发分发总量守恒
- 心跳按 runner_id 批量续期；失联判定 DB 时钟；Enqueue 宿主便利入口
- TaskQueue doc 补时序约束段落：T_heartbeat ≪ staleAfter 量化建议（≥5×，
  argus 90s/10s 在役配比）、排水窗口心跳续期承诺（hbCtx 独立设计意图）、
  staleAfter 与任务执行时长约束
测试依赖 modernc.org/sqlite（纯 Go 无 CGO，钉 v1.34.5 防 go 指令被顶起；
lib/pq 测试依赖同款纪律，生产代码零新增依赖）；AGENTS.md / README 包表更新。
全仓 go build / vet / -race -count=1 绿（pglease worker sqllease sqlqueue 四包）。
关联 oagentkit/queue#24 oagentkit/queue#25 oagentkit/queue#26
* style(worker): gofmt 对齐 LeaseStore doc
* fix(lint): cleanup 里 db.Close 走显式忽略；删 sqlqueue 未用的 nowPlus（golangci errcheck/unused）
---------
Co-authored-by: Murphy-Yi <zhangyi@Murphy-YideMac-Mini.local>

### 修复(worker)：NewLeaderElector 构造期参数校验——ttl/interval 非正与空 key fail-fast（#27）

interval<=0 此前在后台竞选 goroutine 的 time.NewTicker 处运行时 panic——
进程直接被炸，调用方毫无拦截机会（启动后才炸的延迟 panic，不是构造期
fail-fast）。构造期校验 ttl>0 / interval>0 / key 非空（panic 对齐本仓
fail-fast 哲学，但发生在装配期——参数来自配置文件的宿主装配即得反馈）。
TestNewLeaderElectorRejectsInvalidParams：三非法参数逐点钉子 + 合法参数
Start/Stop 不误伤。
关联 oagentkit/queue#27
Co-authored-by: Murphy-Yi <zhangyi@Murphy-YideMac-Mini.local>

- 杂务(deps)：依赖地板追到最新——github.com/oekit/concurrency v0.25.1

| 依赖 | 现地板 | 抬到 |
|---|---|---|
| `github.com/oekit/concurrency` | `v0.24.0` | `v0.25.1` |
| `github.com/oekit/logx` | `v0.8.2` | `v0.8.3` |
## 为什么
矩阵的**发版时刻地板时效**门禁（`verify_tag_floor --ref HEAD`）要求：各仓 HEAD
（= 下次发版会发出去的内容）的依赖地板，相对那一刻的依赖最新版本，不能已落后。
oekit 基座与矩阵模块都在高频发版 ⇒ 地板是**跑步机**，落后一格就会判红。
## 兼容性实测
本工具在提交前跑完 `go build` / `go vet` / `go test -race -count=1 ./...`（全绿才提交）
与 `go mod tidy -diff`（无漂移）⇒ 改动**只影响依赖版本**，零行为差异。
只改 `go.mod` / `go.sum` ⇒ 不需要 CHANGELOG 条目（版本段由下次发版的 `prewrite` 生成）。
Co-authored-by: Murphy-Yi <zhangyi@Murphy-YideMac-Mini.local>

- 杂务(deps)：依赖地板追到最新——github.com/oekit/concurrency v0.24.0

| 依赖 | 现地板 | 抬到 |
|---|---|---|
| `github.com/oekit/concurrency` | `v0.23.3` | `v0.24.0` |
## 为什么
矩阵的**发版时刻地板时效**门禁（`verify_tag_floor --ref HEAD`）要求：各仓 HEAD
（= 下次发版会发出去的内容）的依赖地板，相对那一刻的依赖最新版本，不能已落后。
oekit 基座与矩阵模块都在高频发版 ⇒ 地板是**跑步机**，落后一格就会判红。
## 兼容性实测
本工具在提交前跑完 `go build` / `go vet` / `go test -race -count=1 ./...`（全绿才提交）
与 `go mod tidy -diff`（无漂移）⇒ 改动**只影响依赖版本**，零行为差异。
只改 `go.mod` / `go.sum` ⇒ 不需要 CHANGELOG 条目（版本段由下次发版的 `prewrite` 生成）。
Co-authored-by: Murphy-Yi <zhangyi@Murphy-YideMac-Mini.local>


## [v0.6.6] — 2026-11-04

### 重构(worker)：panic 收编收口 async.Runner、心跳循环收编 loop.Run

泛化 v0.6.4 的 task_panic 手搓 debug.Stack 为 Runner 方案（PanicError.Stack 同源），
四处 panic 隔离统一收口；心跳循环改 loop.Run（ctx 感知+忙时丢 tick+单轮 panic
收编+停机排水）——修复心跳循环单轮 panic 静默死亡（协程死则本实例全部在途任务
90s 后被对端误判过期）；worker 循环体 panic 路由组件 logger（新键
agentkit.worker.loop_panic）。站点日志键与错误文案逐字保留。
deps：oekit/concurrency v0.23.3、oekit/logx v0.8.2

- 杂务(deps)：依赖地板追到最新——github.com/oekit/concurrency v0.23.2

| 依赖 | 现地板 | 抬到 |
|---|---|---|
| `github.com/oekit/concurrency` | `v0.23.1` | `v0.23.2` |
## 为什么
矩阵的**发版时刻地板时效**门禁（`verify_tag_floor --ref HEAD`）要求：各仓 HEAD
（= 下次发版会发出去的内容）的依赖地板，相对那一刻的依赖最新版本，不能已落后。
oekit 基座与矩阵模块都在高频发版 ⇒ 地板是**跑步机**，落后一格就会判红。
## 兼容性实测
本工具在提交前跑完 `go build` / `go vet` / `go test -race -count=1 ./...`（全绿才提交）
与 `go mod tidy -diff`（无漂移）⇒ 改动**只影响依赖版本**，零行为差异。
只改 `go.mod` / `go.sum` ⇒ 不需要 CHANGELOG 条目（版本段由下次发版的 `prewrite` 生成）。

- 杂务(deps)：依赖地板追到最新——github.com/oekit/concurrency v0.23.1

oekit 基座又发了一版（`concurrency` v0.23.1，2026-10-29 02:19 CST）⇒ 上一轮抬到
v0.23.0 的地板已经落后 ⇒ 再抬一格。
本仓只用到 `concurrency` 的少量 API ⇒ 实测 go build / go vet / go test -race 全绿，
**零行为差异**；go 指令未被顶起。
只改 go.mod / go.sum ⇒ 不需要 CHANGELOG 条目（版本段由下次发版的 `prewrite` 生成）。

- 杂务(deps)：依赖地板追到最新——github.com/oekit/concurrency v0.23.0 github.com/oekit/logx v0.8.0

oekit 基座发了新版本（pkg v0.31.0 / concurrency v0.23.0 / logx v0.8.0）⇒ 本仓的
依赖地板落后 ⇒ 抬到最新，让**下一次发版**不再带出落后地板（打 tag 时刻的地板
必须是当时的最新——`verify_tag_floor` / `release.py doctor` 都在查这条）。
兼容性实测：本仓只用这些基座包的少量 API ⇒ go build / go vet / go test -race
全绿，**零行为差异**；go 指令未被顶起（本仓的 go 地板不动）。


## [v0.6.5] — 2026-10-26

- 杂务(deps)：依赖地板追平上游本轮新版本——oekit/concurrency v0.22.0

v0.6.4 打 tag 时本仓 go.mod 钉的是 oekit/concurrency v0.19.0，而那时上游最新已是 v0.22.0
⇒ 该 tag 的「发版时刻地板时效」不达标（matrix-tools 的 Release floor 门禁判红）。
tag 不可重打 ⇒ 只能靠后续版本补齐。本版只抬基座地板，不改任何行为：
- github.com/oekit/concurrency v0.19.0 → v0.22.0


## [v0.6.4] — 2026-10-26

### 修复(worker)：task_panic 日志补全栈——panic 值单行常不足以定位

下游实弹案例:任务体内一处 nil map 赋值,日志只有「assignment to entry in
nil map」一行,调用方排查被迫临时 defer recover+debug.Stack 二次复现才拿到
位置。debug.Stack 成本仅发生在 panic 路径,零热路径开销。

- 文档(queue)：刷正 README.md 的层位声明——tier 0 → tier 2

## 现状
本仓 `README.md` 的两处层位声明写的是**层位重定位之前**的旧层位（tier 0），
而矩阵单一数据源 `matrix-tools/modules.json` 说本仓是 **tier 2（集成层）**。
层位重定位的依据见 matrix-tools 提交 `6d800c4`「层位 = 依赖地板」——
tier 2 的定义是「依赖 tier0/tier1 + 重第三方地板」（`modules.json` 的 `tiers` 映射）。
| 位置 | 改前 | 改后 |
|---|---|---|
| `README.md` 第 3 行（定位声明行） | `**tier 0**` | `**tier 2**` |
| `README.md` 第 16 行（`## 定位` 自述行） | `**tier 0**` | `**tier 2**` |
- `modules.json`：`queue` 的 `tier` = **2**，`tiers["2"]` = 集成层
- 本仓 `AGENTS.md` 第 5 行：**已是** `> 矩阵分层：**tier 2**（集成层，依赖 tier0/tier1 + 重第三方地板）`
  ⇒ 改之前，本仓的**两个第一入口互相矛盾**（`AGENTS.md` 说 tier 2、`README.md` 说 tier 0）
## 影响
`README.md` 是仓级的第一入口（人读 + agent 读）。它写 tier 0 而数据源是 tier 2 ⇒
读者按 README 判断依赖方向就会算错，而分层是矩阵的**硬不变量**（tier N 不得依赖 tier > N）。
## 改动
**只改层位数字与层名，其余段落逐字不动**：
- 第 3 行：`**tier 0**` → `**tier 2**`，层名 `（能力层，依赖 tier0）` → `（集成层，依赖 tier0/tier1 + 重第三方地板）`
- `## 定位`：`**tier 0**` → `**tier 2**`，层名 `能力层` → `集成层`
「零矩阵内依赖」这句是**真**的（`modules.json` 里 `queue.dependsOn` 为空、`go.mod` 里也没有任何矩阵内仓），保留不动，只改层位数字；那一行本来就没有层名，不硬加。
层名与整段括号说明对齐到 tier 2 的权威表述后，与本仓 `AGENTS.md`
及 `llm` / `toolgate` / `tools` / `agent` 四个 tier 2 仓的 README 逐字一致。
diff 只含层位行，`README.md` 的手写段落逐字未动。
## 验收
- [x] `python3 matrix-tools/scripts/verify_repo_tiers.py --root .` ⇒ 本仓 README **红 0**（本地实跑，26 处声明全一致）
- [x] 层位数字与层名与 `modules.json` 一致，且与本仓 `AGENTS.md` 一致
- [x] **纯 .md 改动** ⇒ 不需要 CHANGELOG 条目、不打 tag、不发版（无发版步骤）
关联 oagentkit/matrix-tools#123

- 文档(queue)：刷正 AGENTS.md 层位声明——tier 0 → tier 2

AGENTS.md 的层位声明与矩阵单一数据源 modules.json 不一致。
- 矩阵分层：**tier 0（基础层）** → **tier 2**（集成层，依赖 tier0/tier1 + 重第三方地板）
- 「本仓是 tier 0，任何矩阵内模块都不得被本仓依赖」→ tier 2 的依赖方向表述
- 发版顺序一节里「本仓是 tier 0 基础层，全矩阵都依赖它」→ 按 tier 2 改写
  （本仓带 github.com/lib/pq 驱动地板，矩阵内暂无仓依赖它）
modules.json 里 queue 的 tier = 2（层名「集成层」，出自 tiers 映射）。
层位写错会让人按 tier 0 的口径判依赖方向，而分层是矩阵的硬不变量。
属 matrix-tools#109 的仓级层位漂移；同批修 obs / skillkit / mcpkit / rag。
diff 只含层位行，AGENTS.md 的手写段落逐字未动。


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
