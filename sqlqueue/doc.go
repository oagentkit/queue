// Package sqlqueue — worker.TaskQueue 的 database/sql 参考实现（三方言）。
//
// DB 即队列的三方法（ClaimNextPending / TouchRunningHeartbeats / 重置失联）
// 在 PostgreSQL / MySQL / SQLite 上共一套逻辑，方言差异收在 Dialect 的
// SQL 表达式里。宿主传入 *sql.DB 与 runner id 即用；argus 的 DB 即队列
// 实现（~200 行、三方言在役）是本包的素材来源（oagentkit/queue#25）——
// 自实现宿主可对照本包 + worker.TaskQueue doc 的时序约束段落对齐。
//
// # 纪律（双跑 = 双烧 LLM，写错后果比租约更重）
//
//   - Claim 的原子性：候选 SELECT 只是**提示**，判定与状态迁移在**单语句
//     守卫 UPDATE**（WHERE task_id=? AND status='pending'）里完成——多副本
//     双抢时败者 RowsAffected=0 重试下一候选，绝无两副本同持一个任务；
//     MySQL 的「同表 UPDATE 子查询」限制经派生表包裹绕开（本实现的候选
//     读取独立于 UPDATE，天然不受 1093 约束）；
//   - DB 时钟：heartbeat_at 写入与 stale 判定同钟（与 worker.LeaseStore
//     红线同源——应用时钟漂移会误判失联 → 他副本接手 → 同一任务双跑）；
//   - 心跳批量续期按 runner_id 单条 UPDATE（索引面见 Migrate 的两个索引）；
//   - 方言分叉：SQLite 单写模型无需 SKIP LOCKED；MySQL/PG 高并发场景宿主
//     可按需切 FOR UPDATE SKIP LOCKED 形态——本参考实现以可移植为先。
//
// # 毒任务：重试上限与死信终态（oagentkit/queue#70）
//
// attempts 计数器沿用既有口径：每次 ClaimNextPending 与每次失联复位各 +1
// （TaskQueue 三方法面拿不到它——观测缺口由 worker.DetailedClaimer 可选接口
// 补上，领取侧直接拿计数）。不设上限时毒任务（Run 恒 panic/报错的任务）会
// 永远循环：claim → 失败 → 心跳冻结 → staleAfter 后复位 → FIFO 又排到队首。
//
//   - WithMaxAttempts(n)（n>0）：失联复位时计数已到限（attempts ≥ n）的行
//     置入**终态** StatusDeadLetter 而非 pending——毒任务在有限轮内退出循环。
//     零值（缺省）不封顶，保持既有行为（兼容性红线，但强烈建议宿主显式设 n）；
//   - 死信面：状态 'dead_letter' 可直查（监控/告警/排查），claim 只取 pending，
//     死信行永不再被领取、也不再被复位；库不自动重驱，人工重驱 =
//     `UPDATE ... SET status='pending', attempts=0`；
//   - 消费侧闸门：宿主对 Queue 类型断言 worker.DetailedClaimer，从
//     ClaimNextPendingDetailed 拿 attempts 自行裁决（不碰表内状态）。
//
// # 构造期闸门（oagentkit/queue#69）
//
// New 对 nil db 与空 runnerID 直接 panic（与 worker.NewLeaderElector 同一道
// fail-fast 纪律）：runnerID 是**副本身份**——心跳归属（WHERE runner_id = ?）
// 与失联复位的键，必须每实例唯一；空身份让多副本互相续命/互相复位且零报错
// （queue#43 空身份家族、queue#42 静默丢失同型）。nil db 只会把 panic 推迟到
// 首次用语句的后台 goroutine。
package sqlqueue
