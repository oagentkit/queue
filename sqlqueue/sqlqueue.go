package sqlqueue

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"time"
)

// DefaultTable 缺省任务表名。
const DefaultTable = "agentkit_task"

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
}

// New 构造（表名用 DefaultTable；WithTable 覆盖）。
func New(db *sql.DB, dialect Dialect, runnerID string) *Store {
	return &Store{db: db, dialect: dialect, table: DefaultTable, runnerID: runnerID}
}

// WithTable 指定任务表名（非空且匹配标识符白名单才生效）。
func (s *Store) WithTable(name string) *Store {
	if name != "" && tableIdentRe.MatchString(name) {
		s.table = name
	}
	return s
}

func (s *Store) ph(i int) string {
	if s.dialect == Postgres {
		return fmt.Sprintf("$%d", i)
	}
	return "?"
}

func (s *Store) nowPlus(phTTL string) string {
	switch s.dialect {
	case Postgres:
		return fmt.Sprintf("now() + make_interval(secs => %s)", phTTL)
	case MySQL:
		return fmt.Sprintf("DATE_ADD(NOW(), INTERVAL %s SECOND)", phTTL)
	default:
		return fmt.Sprintf("strftime('%%s','now') + %s", phTTL)
	}
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
	for i := 0; i < claimRetries; i++ {
		var candidate string
		q := fmt.Sprintf(`SELECT task_id FROM %s WHERE status = 'pending'
ORDER BY created_at, task_id LIMIT 1`, s.table)
		if err := s.db.QueryRowContext(ctx, q).Scan(&candidate); err != nil {
			if err == sql.ErrNoRows {
				return "", false, nil
			}
			return "", false, err
		}
		q = fmt.Sprintf(`UPDATE %s SET status = 'running', runner_id = %s,
heartbeat_at = %s, attempts = attempts + 1
WHERE task_id = %s AND status = 'pending'`,
			s.table, s.ph(1), s.nowFn(), s.ph(2))
		res, err := s.db.ExecContext(ctx, q, s.runnerID, candidate)
		if err != nil {
			return "", false, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return "", false, err
		}
		if n > 0 {
			return candidate, true, nil
		}
		// 候选被他副本抢先 → 重试下一候选
	}
	return "", false, nil
}

// TouchRunningHeartbeats 续期本实例 running 任务的心跳（批量单 UPDATE）。
func (s *Store) TouchRunningHeartbeats(ctx context.Context) error {
	q := fmt.Sprintf(`UPDATE %s SET heartbeat_at = %s
WHERE runner_id = %s AND status = 'running'`, s.table, s.nowFn(), s.ph(1))
	_, err := s.db.ExecContext(ctx, q, s.runnerID)
	return err
}

// ResetRunningToPending 重置心跳已死的 running 任务回 pending（返回条数）。
// 失联判定走 DB 时钟（heartbeat_at < now - staleAfter）。
func (s *Store) ResetRunningToPending(ctx context.Context, staleAfter time.Duration) (int64, error) {
	q := fmt.Sprintf(`UPDATE %s SET status = 'pending', runner_id = NULL, attempts = attempts + 1
WHERE status = 'running' AND heartbeat_at < %s`, s.table, s.nowMinus(s.ph(1)))
	res, err := s.db.ExecContext(ctx, q, staleAfter.Seconds())
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
