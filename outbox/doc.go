// Package outbox —— 统一副作用 outbox 队列（durable side-effect queue），
// 规格驱动：宿主按 kind 注册退避规格与处理器，机制面只认规格不认业务。
//
// 定位与 sqlqueue 划清边界（oagentkit/queue#131）：sqlqueue 是**任务认领**
// （claim/心跳/失联复位，task_id 主键，重入即冲突）；outbox 是**副作用投递**，
// 三件套是它和任务队列的分界——
//
//   - 同 (kind, key) 幂等 upsert：重入即**替换**（载荷更新、状态回 pending、
//     尝试计数清零），不是主键冲突；
//   - not_before 延迟可见调度：行到点才可被认领（claim 只取 not_before<=now），
//     失败退避与显式延后都落在这一列上；
//   - 「推后 ≠ 失败」：Later() 与 handler 返回 DeferError 都只推 not_before、
//     不动 attempts；per-kind 退避规格表（Spec）决定失败重试的节奏。
//
// 典型宿主：通知/回写/转发型服务（IM 推送卡、工单回写、告警转发）——
// 「先落库再执行、同键重入替换、失败退避、死信可见」，替代 fire-and-forget
// 丢单或各自手写重试。
//
// # 投递语义（at-least-once，handler 必须幂等）
//
// 副作用在 handler 调用里发生，行账在 DB 里记账——两者不可能原子，本包的
// 契约是**至少一次投递**：
//
//   - 执行成功清账（删行）；清账是 token 守卫的条件删除，账没删掉而行已被
//     同键重入替换时，新载荷会被下一轮认领再执行——至多一次陈旧副作用 +
//     至少一次最新副作用；
//   - 进程崩溃遗留的 running 行由 stale 兜底回收（attempts+1 重投，达上限
//     进死信）；WithStaleAfter 必须**大于 handler 最长耗时**，否则慢 handler
//     会被误判失联造成同键双执行；
//   - 多实例可共享同一张表（claim 是单语句 CAS，败者换下一候选），但共享
//     前提同样是 handler 幂等。
//
// # DB 时钟红线（与 sqlqueue 同源）
//
// not_before 的写入（延迟/退避）与到期判定（claim 的 WHERE not_before <= now）
// 全部用**数据库时钟**表达式，应用时钟不参与——多实例时钟漂移只会影响调度
// 精度，不会造成双执行或永不可见。
//
// # 死信与人工干预
//
// 状态常量（StatusPending/StatusRunning/StatusDeadLetter）导出供宿主直查：
// 死信行可 `SELECT ... WHERE status = 'dead_letter'`；人工重驱 =
// `UPDATE ... SET status='pending', attempts=0, not_before=<now>`；同键重新
// Enqueue 亦会复活死信行（最新意图优先，重入即替换语义的自然结果）。
// Health() 是宿主诊断面的快捷入口（三态计数）。
//
// # 装配纪律（构造期 fail-fast，oagentkit/queue#69 同一道）
//
//   - New 对 nil db 直接 panic；Register 对空 kind、超长 kind、重复 kind、
//     nil handler 直接 panic——装配期语义破坏必须当场暴露，运行期拦截只会
//     把 panic 推迟到后台 goroutine；
//   - kind 规格表是**宿主策略**（哪些 kind、并发多少、退避多长），机制只收
//     规格驱动的 worker——本包不内置任何业务 kind；
//   - Run 前必须 Register 且先 Migrate；Run 重入（同一 Queue 并行两跑）直接
//     panic，双跑 = 同键双执行。
//
// # 存储与方言
//
// 表结构、Migrate 的幂等建表与 MySQL information_schema 判存沿 sqlqueue
// 先例（queue#85/#87）：列宽由 MaxKindLen/MaxKeyLen 派生 DDL，护栏与 DDL
// 同源；三方言（Postgres/MySQL/SQLite）共一套逻辑，方言差异收在 Dialect
// 的 SQL 表达式里。延迟/退避按 DB 时钟秒级粒度（MySQL DATETIME 无小数秒）。
package outbox
