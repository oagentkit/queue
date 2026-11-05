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
package sqlqueue
