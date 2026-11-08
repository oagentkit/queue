package sqlqueue

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// DefaultTable 缺省任务表名。
const DefaultTable = "agentkit_task"

// 任务行状态（宿主直查/监控用；done/failed 等业务终态由宿主自写，不入此列）。
const (
	// StatusPending 待领取（入队初态；失联复位的落点）。
	StatusPending = "pending"
	// StatusRunning 已被某副本领取、执行中（heartbeat_at 是存活信号）。
	StatusRunning = "running"
	// StatusDeadLetter 终态：毒任务重试超限后的归宿（queue#70）。claim 只取
	// pending，死信行就此退出重试循环；库不自动重驱，人工重驱 =
	// `UPDATE ... SET status='pending', attempts=0`。
	StatusDeadLetter = "dead_letter"
)

// claimRetries 单轮 Claim 的竞态重试上限：候选被抢 → 换下一候选；上限内
// 全部被抢视作本轮无任务（下轮守护循环再试）。正常负载下一两次即命中。
const claimRetries = 8

// tableIdentRe 表名安全白名单（防拼接注入，sqllease/pglease 同款）。
var tableIdentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Dialect SQL 方言（Postgres / MySQL / SQLite）。
type Dialect int

const (
	// Postgres PostgreSQL（占位符 $N，now() + make_interval）。
	Postgres Dialect = iota
	// MySQL MySQL / MariaDB（占位符 ?，NOW() ± INTERVAL）。
	MySQL
	// SQLite SQLite（占位符 ?，strftime('%s','now') unixepoch REAL）。
	SQLite
)

// Store worker.TaskQueue 的 SQL 参考实现（三方言）。runnerID 标识本实例
// （心跳归属与失联判定的主体）。
type Store struct {
	db       *sql.DB
	dialect  Dialect
	table    string
	runnerID string
	// maxAttempts 重试计数上限（queue#70）：>0 时失联复位把计数已到限的行
	// 置入终态 dead_letter 而非 pending；0（缺省）不封顶——既有行为。
	maxAttempts int
}

// New 构造（表名用 DefaultTable；WithTable 覆盖；重试上限可选 WithMaxAttempts）。
//
// runnerID 是**副本身份**（心跳归属 `WHERE runner_id = ?` 与失联复位的键），
// 必须每实例唯一且非空：空身份让多副本在心跳/复位层互相认作对方——互相续命
// （死副本的任务永不复位，静默丢失，queue#42 形态）或互相复位（误判失联），
// 且全程零报错（queue#43 空身份家族）。
//
// 构造期 fail-fast（panic，与 worker.NewLeaderElector 同一道纪律，
// oagentkit/queue#69）：nil db 与空 runnerID 都是装配期语义破坏——nil db
// 只会把 panic 推迟到首次用语句的 goroutine（常在后台），空身份则静默失效。
func New(db *sql.DB, dialect Dialect, runnerID string) *Store {
	if db == nil {
		panic("sqlqueue: New db 不能为 nil——nil *sql.DB 只会把 panic 推迟到首次用语句的 goroutine（常在后台，调用方毫无拦截机会），必须在构造期拦截")
	}
	if runnerID == "" {
		panic("sqlqueue: New runnerID 不能为空——runnerID 是副本身份（心跳归属 WHERE runner_id=? 的键），空身份让多副本互相续命/互相复位且零报错（oagentkit/queue#43 同族、queue#42 静默丢失形态），必须每实例唯一")
	}
	return &Store{db: db, dialect: dialect, table: DefaultTable, runnerID: runnerID}
}

// WithTable 指定任务表名（非空且匹配标识符白名单才生效）。
func (s *Store) WithTable(name string) *Store {
	if name != "" && tableIdentRe.MatchString(name) {
		s.table = name
	}
	return s
}

// WithMaxAttempts 设置重试计数上限，返回自身（链式；n<=0 = 不封顶，即缺省行为）。
//
// 契约（oagentkit/queue#70）：attempts 计数器沿用既有口径——每次 claim 与每次
// 失联复位各 +1。n>0 时，ResetRunningToPending 复位失联 running 行时若该行
// attempts 已 ≥ n，不再回 pending 而是置入终态 dead_letter（归属清空、计数
// 随最后一次复位定格）——毒任务（Run 恒 panic/报错）在有限轮内退出重试循环，
// 不再每轮烧一次 claim+run。死信面与观测：
//
//   - 死信行可直查：`SELECT ... WHERE status = 'dead_letter'`（状态常量
//     StatusDeadLetter），监控/告警/人工排查都吃这一面；
//   - 人工重驱：`UPDATE ... SET status = 'pending', attempts = 0`；
//   - 不想碰表内状态的宿主：实现/探测 worker.DetailedClaimer，领取侧直接
//     拿 attempts。
//
// 零值（不调用本方法）**保持既有行为**：不封顶，毒任务照旧无限重试——
// 兼容性红线。建议宿主一律显式设 n（按 staleAfter 周期与业务可容忍重试轮数折算）。
func (s *Store) WithMaxAttempts(n int) *Store {
	if n > 0 {
		s.maxAttempts = n
	} else {
		s.maxAttempts = 0
	}
	return s
}

func (s *Store) ph(i int) string {
	if s.dialect == Postgres {
		return fmt.Sprintf("$%d", i)
	}
	return "?"
}

func (s *Store) nowMinus(phSecs string) string {
	switch s.dialect {
	case Postgres:
		return fmt.Sprintf("now() - make_interval(secs => %s)", phSecs)
	case MySQL:
		return fmt.Sprintf("DATE_SUB(NOW(), INTERVAL %s SECOND)", phSecs)
	default:
		return fmt.Sprintf("strftime('%%s','now') - %s", phSecs)
	}
}

func (s *Store) nowFn() string {
	if s.dialect == SQLite {
		return "strftime('%s','now')"
	}
	return "NOW()" // PG/MySQL 大小写均可
}

// ClaimNextPending 原子抢占下一个 pending 任务（worker.TaskQueue 实现）。
// 候选 SELECT（created_at FIFO）只是提示；状态迁移在单语句守卫 UPDATE
// （WHERE task_id=? AND status='pending'）里完成——竞态败者重试下一候选，
// 双副本不可能同持一个任务。
func (s *Store) ClaimNextPending(ctx context.Context) (string, bool, error) {
	id, _, ok, err := s.ClaimNextPendingDetailed(ctx)
	return id, ok, err
}

// ClaimNextPendingDetailed 是 ClaimNextPending 的加强版：额外返回领取时点的
// attempts 计数（worker.DetailedClaimer 实现，oagentkit/queue#70）。
//
// attempts 是**含本次领取**的值（候选行快照 +1，守卫 UPDATE 的自增即本调用的
// 写入）：首次领取 = 1，失联复位一轮后重领 = 3（claim+reset+claim）。宿主据此
// 可在消费侧实现自己的毒任务闸门（例如 attempts > N 时改走本地死信处理），
// 无需直查内部表。ok=false 时 attempts 无意义（恒 0）。
//
// 守卫 UPDATE 语义与 ClaimNextPending 完全一致：候选 SELECT 与守卫 UPDATE 之间
// 不可能有并发写改动 pending 行的 attempts（能改它的只有 claim 类守卫 UPDATE，
// 而那会让本调用的守卫失败进下一候选），故返回值即真实落库值。
func (s *Store) ClaimNextPendingDetailed(ctx context.Context) (string, int, bool, error) {
	for i := 0; i < claimRetries; i++ {
		var candidate string
		var seen int
		q := fmt.Sprintf(`SELECT task_id, attempts FROM %s WHERE status = '%s'
ORDER BY created_at, task_id LIMIT 1`, s.table, StatusPending)
		if err := s.db.QueryRowContext(ctx, q).Scan(&candidate, &seen); err != nil {
			if err == sql.ErrNoRows {
				return "", 0, false, nil
			}
			return "", 0, false, err
		}
		q = fmt.Sprintf(`UPDATE %s SET status = '%s', runner_id = %s,
heartbeat_at = %s, attempts = attempts + 1
WHERE task_id = %s AND status = '%s'`,
			s.table, StatusRunning, s.ph(1), s.nowFn(), s.ph(2), StatusPending)
		res, err := s.db.ExecContext(ctx, q, s.runnerID, candidate)
		if err != nil {
			return "", 0, false, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return "", 0, false, err
		}
		if n > 0 {
			return candidate, seen + 1, true, nil
		}
		// 候选被他副本抢先 → 重试下一候选
	}
	return "", 0, false, nil
}

// TouchRunningHeartbeats 续期本实例 running 任务的心跳（批量单 UPDATE）。
//
// 这是 worker.TaskQueue 的盲续语义：**不区分「我在跑它」与「它归我」**，
// 故 runTask 已退出的任务也会被续活（oagentkit/queue#42）。Pool 检测到本类型
// 实现了 worker.ScopedHeartbeater（见下）后不再调用本方法；保留它是为了
// 接口完整性与「不用 Pool、直接驱动队列」的宿主。
func (s *Store) TouchRunningHeartbeats(ctx context.Context) error {
	q := fmt.Sprintf(`UPDATE %s SET heartbeat_at = %s
WHERE runner_id = %s AND status = 'running'`, s.table, s.nowFn(), s.ph(1))
	_, err := s.db.ExecContext(ctx, q, s.runnerID)
	return err
}

// TouchHeartbeatsFor 只续期 taskIDs 中属于本实例且在跑的任务心跳
// （worker.ScopedHeartbeater 实现）。
//
// 三重守卫缺一不可：runner_id 防续他实例的行，status='running' 防把已终态
// 的行续回新鲜，task_id IN (...) 是本次修复的实质——不在续期集里的行
// （runTask 已 panic/报错/成功退出的）心跳就此冻结，staleAfter 后由
// ResetRunningToPending 复位重跑。
func (s *Store) TouchHeartbeatsFor(ctx context.Context, taskIDs []string) error {
	if len(taskIDs) == 0 {
		// 空集绝不能退化成「无过滤条件」——那是盲续，正是要修的东西
		return nil
	}
	marks := make([]string, len(taskIDs))
	args := make([]any, 0, len(taskIDs)+1)
	args = append(args, s.runnerID) // 占位符 1 是 runner_id
	for i, id := range taskIDs {
		marks[i] = s.ph(i + 2)
		args = append(args, id)
	}
	q := fmt.Sprintf(`UPDATE %s SET heartbeat_at = %s
WHERE runner_id = %s AND status = 'running' AND task_id IN (%s)`,
		s.table, s.nowFn(), s.ph(1), strings.Join(marks, ", "))
	_, err := s.db.ExecContext(ctx, q, args...)
	return err
}

// ResetRunningToPending 重置心跳已死的 running 任务（返回处理条数：回 pending
// 与进死信的行都计入）。失联判定走 DB 时钟（heartbeat_at < now - staleAfter）。
//
// 重试上限（WithMaxAttempts，oagentkit/queue#70）：设了上限（>0）时，计数已到
// 限（attempts ≥ n）的失联行不再回 pending，而是置入**终态** dead_letter——
// 毒任务在有限轮内退出「复位→重领→再失败」循环。计数未到限的行照旧回 pending
// 重试。零值（缺省）不封顶：全部回 pending，与既有行为逐字一致。
//
// attempts 两种落点都 +1（计数口径不变：claim 与 reset 各计一次）；dead_letter
// 行的归属照常清空，计数随最后一次复位定格，后续 claim/reset 不再碰它。
func (s *Store) ResetRunningToPending(ctx context.Context, staleAfter time.Duration) (int64, error) {
	var q string
	var args []any
	if s.maxAttempts > 0 {
		q = fmt.Sprintf(`UPDATE %s SET status = CASE WHEN attempts >= %s THEN '%s' ELSE '%s' END,
runner_id = NULL, attempts = attempts + 1
WHERE status = '%s' AND heartbeat_at < %s`,
			s.table, s.ph(1), StatusDeadLetter, StatusPending, StatusRunning, s.nowMinus(s.ph(2)))
		args = []any{s.maxAttempts, staleAfter.Seconds()}
	} else {
		q = fmt.Sprintf(`UPDATE %s SET status = '%s', runner_id = NULL, attempts = attempts + 1
WHERE status = '%s' AND heartbeat_at < %s`,
			s.table, StatusPending, StatusRunning, s.nowMinus(s.ph(1)))
		args = []any{staleAfter.Seconds()}
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Enqueue 追加一个 pending 任务（TaskQueue 接口外的宿主便利入口：
// 接口只约定消费面三方法，入队由宿主直插本表——本方法是等价快捷方式）。
func (s *Store) Enqueue(ctx context.Context, taskID string) error {
	q := fmt.Sprintf(`INSERT INTO %s (task_id, status, runner_id, heartbeat_at, created_at, attempts)
VALUES (%s, 'pending', NULL, NULL, %s, 0)`, s.table, s.ph(1), s.nowFn())
	_, err := s.db.ExecContext(ctx, q, taskID)
	return err
}

// Migrate 建任务表与两个索引（幂等；MySQL 无 CREATE INDEX IF NOT EXISTS，
// 走 information_schema 判存）。
func (s *Store) Migrate(ctx context.Context) error {
	ts := map[Dialect]string{Postgres: "TIMESTAMPTZ", MySQL: "DATETIME", SQLite: "REAL"}[s.dialect]
	createIdx := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s_status_created ON %s (status, created_at)`, s.table, s.table)
	createHB := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s_runner_status ON %s (runner_id, status)`, s.table, s.table)
	if s.dialect == MySQL {
		createIdx = fmt.Sprintf(`ALTER TABLE %s ADD INDEX %s_status_created (status, created_at)`, s.table, s.table)
		createHB = fmt.Sprintf(`ALTER TABLE %s ADD INDEX %s_runner_status (runner_id, status)`, s.table, s.table)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
  task_id     VARCHAR(64) PRIMARY KEY,
  status      VARCHAR(16) NOT NULL DEFAULT 'pending',
  runner_id   VARCHAR(128),
  heartbeat_at %s,
  created_at  %s NOT NULL,
  attempts    INT NOT NULL DEFAULT 0
)`, s.table, ts, ts)); err != nil {
		return err
	}
	for _, q := range []string{createIdx, createHB} {
		if s.dialect == MySQL {
			var n int
			if err := s.db.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM information_schema.statistics WHERE table_name = ? AND index_name = ?`,
				s.table, indexName(q)).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				continue // 已存在，跳过
			}
		}
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// indexName 从 ALTER 语句里取索引名（MySQL 判存用）。
func indexName(alter string) string {
	// "ALTER TABLE t ADD INDEX t_runner_status (...)" → 第 4 个空格后的 token 链
	const marker = " ADD INDEX "
	i := len(alter) - len(marker)
	for j := 0; j < len(alter)-len(marker); j++ {
		if alter[j:j+len(marker)] == marker {
			i = j + len(marker)
			break
		}
	}
	rest := alter[i:]
	for j := 0; j < len(rest); j++ {
		if rest[j] == ' ' || rest[j] == '(' {
			return rest[:j]
		}
	}
	return rest
}
