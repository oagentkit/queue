package outbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Dialect SQL 方言（Postgres / MySQL / SQLite）。与 sqllease/sqlqueue 的
// Dialect 各自独立（仓内子包零互相依赖的既有形态），字面值同一序。
type Dialect int

const (
	// Postgres PostgreSQL（占位符 $N，now() ± make_interval）。
	Postgres Dialect = iota
	// MySQL MySQL / MariaDB（占位符 ?，NOW() ± INTERVAL）。
	MySQL
	// SQLite SQLite（占位符 ?，strftime('%s','now') unixepoch REAL）。
	SQLite
)

// defaultStaleAfter / defaultPollInterval 缺省兜底与轮询档。
const (
	defaultStaleAfter   = 60 * time.Second
	defaultPollInterval = 500 * time.Millisecond
)

// secs 子秒精度入参（延迟/退避/stale 窗口以秒为单位交给 SQL 表达式）。
func secs(d time.Duration) float64 { return d.Seconds() }

// ---- SQL 构建器（纯文本，方言分叉只在这里；store_sql_test.go 逐方言钉文本）----

// ph 方言占位符（PG 用 $N 序号，MySQL/SQLite 用 ?）。
func (q *Queue) ph(i int) string {
	if q.dialect == Postgres {
		return fmt.Sprintf("$%d", i)
	}
	return "?"
}

// nowFn DB 时钟表达式（not_before 写入与到期判定同钟——应用时钟不参与，
// 红线见 doc「DB 时钟红线」节）。
func (q *Queue) nowFn() string {
	if q.dialect == SQLite {
		return "strftime('%s','now')"
	}
	return "NOW()" // PG/MySQL 大小写均可
}

// plusSecs / minusSecs now() ± 参数秒（延迟、退避、stale 判定共用）。
func (q *Queue) plusSecs(phSecs string) string {
	switch q.dialect {
	case Postgres:
		return fmt.Sprintf("now() + make_interval(secs => %s)", phSecs)
	case MySQL:
		return fmt.Sprintf("DATE_ADD(NOW(), INTERVAL %s SECOND)", phSecs)
	default:
		return fmt.Sprintf("strftime('%%s','now') + %s", phSecs)
	}
}

func (q *Queue) minusSecs(phSecs string) string {
	switch q.dialect {
	case Postgres:
		return fmt.Sprintf("now() - make_interval(secs => %s)", phSecs)
	case MySQL:
		return fmt.Sprintf("DATE_SUB(NOW(), INTERVAL %s SECOND)", phSecs)
	default:
		return fmt.Sprintf("strftime('%%s','now') - %s", phSecs)
	}
}

// upsertSQL 幂等 upsert：同 (kind, key) 重入替换——payload 更新、状态回
// pending、attempts 清零、not_before 重置为 now+delay、认领凭据清空、
// created_at 刷新（最新意图排到队尾）。
//
// 执行中的行同样被替换：claim_token 清空使在途执行的清账（按 token 守卫）
// 落空，行以新载荷重投——在途那次仍以旧载荷跑完（至多一次陈旧副作用），
// 新载荷至少再执行一次（at-least-once 契约，doc「投递语义」节）。
func (q *Queue) upsertSQL() string {
	setClause := fmt.Sprintf(`  payload = excluded.payload,
  status = '%s',
  attempts = 0,
  not_before = excluded.not_before,
  claim_token = NULL,
  claimed_at = NULL,
  created_at = excluded.created_at`, StatusPending)
	conflict := fmt.Sprintf("ON CONFLICT (kind, dedup_key) DO UPDATE SET\n%s", setClause)
	if q.dialect == MySQL {
		setClause = fmt.Sprintf(`  payload = VALUES(payload),
  status = '%s',
  attempts = 0,
  not_before = VALUES(not_before),
  claim_token = NULL,
  claimed_at = NULL,
  created_at = VALUES(created_at)`, StatusPending)
		conflict = fmt.Sprintf("ON DUPLICATE KEY UPDATE\n%s", setClause)
	}
	createdAt := "NOW()"
	if q.dialect == SQLite {
		createdAt = "strftime('%s','now')"
	}
	qSQL := fmt.Sprintf(`INSERT INTO %s (kind, dedup_key, payload, status, attempts, not_before, created_at)
VALUES (%s, %s, %s, '%s', 0, %s, %s)
%s`, q.table, q.ph(1), q.ph(2), q.ph(3), StatusPending, q.plusSecs(q.ph(4)), createdAt, conflict)
	return qSQL
}

// claimCandidateSQL 候选 SELECT（提示面）：到期 pending 行，not_before/
// created_at/dedup_key 序。attempts < 上限挡住预算耗尽的行（正常流里它们
// 在失败路径就已进死信；防御规格收紧的存量行被反复空认领）。
func (q *Queue) claimCandidateSQL() string {
	qSQL := fmt.Sprintf(`SELECT dedup_key FROM %s
WHERE kind = %s AND status = '%s' AND attempts < %s AND not_before <= %s
ORDER BY not_before, created_at, dedup_key LIMIT 1`,
		q.table, q.ph(1), StatusPending, q.ph(2), q.nowFn())
	return qSQL
}

// claimGuardSQL 认领 CAS：pending → running 单语句守卫，多实例/多 worker
// 双抢时败者 RowsAffected=0 换下一候选，绝无两 worker 同持一行。
func (q *Queue) claimGuardSQL() string {
	qSQL := fmt.Sprintf(`UPDATE %s SET status = '%s', claim_token = %s, claimed_at = %s
WHERE kind = %s AND dedup_key = %s AND status = '%s' AND not_before <= %s`,
		q.table, StatusRunning, q.ph(1), q.nowFn(), q.ph(2), q.ph(3), StatusPending, q.nowFn())
	return qSQL
}

// claimRereadSQL 认领命中后的立即重读（sqlqueue#98 同款纪律）：payload/
// attempts 取守卫 UPDATE 之后的库内真值——窗口内的同键重入替换（attempts
// 清零、载荷换新）如实反映，返回值不依赖 SELECT 快照的窗口假设。
func (q *Queue) claimRereadSQL() string {
	qSQL := fmt.Sprintf(`SELECT payload, attempts FROM %s WHERE kind = %s AND dedup_key = %s`,
		q.table, q.ph(1), q.ph(2))
	return qSQL
}

// completeSQL 成功清账：条件删除（token 守卫）。0 行 = 行已被同键重入替换
// （token 清空）——新载荷留待下一轮认领，本次副作用不删账。
func (q *Queue) completeSQL() string {
	qSQL := fmt.Sprintf(`DELETE FROM %s WHERE kind = %s AND dedup_key = %s AND status = '%s' AND claim_token = %s`,
		q.table, q.ph(1), q.ph(2), StatusRunning, q.ph(3))
	return qSQL
}

// failSQL 失败退避：attempts+1、回 pending、not_before = now + backoff
// （调用方按 Spec 算好退避——公式单源在 worker.go 的 retry.Backoff）。
// token 守卫：0 行 = 已被重入替换，本次失败判定作废（新意图的预算是新的）。
// 占位符按**文本出现序**编号（SET 子句的退避秒数排第 1，MySQL/SQLite 的
// 位置占位符才与参数序一致——PG 的 $N 绑定同样按参数序）。
func (q *Queue) failSQL() string {
	qSQL := fmt.Sprintf(`UPDATE %s SET status = '%s', attempts = attempts + 1, claim_token = NULL,
claimed_at = NULL, not_before = %s
WHERE kind = %s AND dedup_key = %s AND status = '%s' AND claim_token = %s`,
		q.table, StatusPending, q.plusSecs(q.ph(1)), q.ph(2), q.ph(3), StatusRunning, q.ph(4))
	return qSQL
}

// deadLetterSQL 失败终态：attempts 达 MaxAttempts 的行置入死信（归属与
// 凭据清空，计数定格）。token 守卫同 failSQL。
func (q *Queue) deadLetterSQL() string {
	qSQL := fmt.Sprintf(`UPDATE %s SET status = '%s', attempts = attempts + 1, claim_token = NULL,
claimed_at = NULL
WHERE kind = %s AND dedup_key = %s AND status = '%s' AND claim_token = %s`,
		q.table, StatusDeadLetter, q.ph(1), q.ph(2), StatusRunning, q.ph(3))
	return qSQL
}

// deferSQL 显式延后（推后 ≠ 失败）：attempts **不动**，只推 not_before 并
// 释放认领。token 守卫同 failSQL；占位符文本序纪律同 failSQL。
func (q *Queue) deferSQL() string {
	qSQL := fmt.Sprintf(`UPDATE %s SET status = '%s', claim_token = NULL, claimed_at = NULL,
not_before = %s
WHERE kind = %s AND dedup_key = %s AND status = '%s' AND claim_token = %s`,
		q.table, StatusPending, q.plusSecs(q.ph(1)), q.ph(2), q.ph(3), StatusRunning, q.ph(4))
	return qSQL
}

// resetStaleSQL stale 兜底：回收 claimed_at 超窗的 running 行（进程崩溃/
// DB 抖动遗留）。回收 = attempts+1 的失败（一次执行机会被烧掉），达
// MaxAttempts 的直接进死信——阈值在 Go 侧折算成 `attempts >= M-1`（行当前
// attempts=f，被回收即第 f+1 次失败，判 f+1 >= M），折算纪律与
// sqlqueue#86 同源（阈值不进 SQL 除法——MySQL 的 `/` 是小数除法）。
func (q *Queue) resetStaleSQL() string {
	qSQL := fmt.Sprintf(`UPDATE %s SET status = CASE WHEN attempts >= %s THEN '%s' ELSE '%s' END,
attempts = attempts + 1, claim_token = NULL, claimed_at = NULL
WHERE kind = %s AND status = '%s' AND claimed_at < %s`,
		q.table, q.ph(1), StatusDeadLetter, StatusPending, q.ph(2), StatusRunning, q.minusSecs(q.ph(3)))
	return qSQL
}

// countSQL Health 的聚合面：单语句 GROUP BY。
func (q *Queue) countSQL() string {
	return fmt.Sprintf(`SELECT status, COUNT(*) FROM %s GROUP BY status`, q.table)
}

// ---- 执行层（构建器的 DB 落地面）----

// upsertRow 执行幂等 upsert。payload 已由 enqueue 层 JSON 编码。
func (q *Queue) upsertRow(ctx context.Context, kind, key, payload string, delay time.Duration) error {
	qSQL := q.upsertSQL()
	_, err := q.db.ExecContext(ctx, qSQL, kind, key, payload, secs(delay))
	return err
}

// claimRow 一次认领的完整凭据。payload/attempts 是守卫 UPDATE 命中后立即
// 重读的库内真值；token 是本次认领的随机凭据，清账/退避/延后全按它守卫。
type claimRow struct {
	kind     string
	key      string
	payload  []byte
	attempts int
	token    string
}

// claimDue 认领一条到期的 pending 行。候选 SELECT 只是提示；竞态败者重试
// 下一候选（上限 claimRetries），上限内全被抢视作本轮无到期行。
func (q *Queue) claimDue(ctx context.Context, kind string, maxAttempts int) (*claimRow, bool, error) {
	candidate := q.claimCandidateSQL()
	for range claimRetries {
		var key string
		if err := q.db.QueryRowContext(ctx, candidate, kind, maxAttempts).Scan(&key); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, false, nil
			}
			return nil, false, err
		}
		token := newClaimToken()
		guard := q.claimGuardSQL()
		res, err := q.db.ExecContext(ctx, guard, token, kind, key)
		if err != nil {
			return nil, false, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return nil, false, err
		}
		if n == 0 {
			continue // 候选被他 worker/他实例抢先 → 换下一候选
		}
		reread := q.claimRereadSQL()
		var payload string
		var attempts int
		if err := q.db.QueryRowContext(ctx, reread, kind, key).Scan(&payload, &attempts); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				// 理论不可达（清账只属于本次 token），防御：视作本轮无行
				return nil, false, nil
			}
			return nil, false, err
		}
		return &claimRow{kind: kind, key: key, payload: []byte(payload), attempts: attempts, token: token}, true, nil
	}
	return nil, false, nil
}

// completeClaimed / failClaimed / deadLetterClaimed / deferClaimed 四个记账
// 面（全部 token 守卫；句法见各构建器 doc）。
func (q *Queue) completeClaimed(ctx context.Context, kind, key, token string) (int64, error) {
	return q.execAccounting(ctx, q.completeSQL(), kind, key, token)
}

func (q *Queue) failClaimed(ctx context.Context, kind, key, token string, backoff time.Duration) (int64, error) {
	return q.execAccounting(ctx, q.failSQL(), secs(backoff), kind, key, token)
}

func (q *Queue) deadLetterClaimed(ctx context.Context, kind, key, token string) (int64, error) {
	return q.execAccounting(ctx, q.deadLetterSQL(), kind, key, token)
}

func (q *Queue) deferClaimed(ctx context.Context, kind, key, token string, delay time.Duration) (int64, error) {
	return q.execAccounting(ctx, q.deferSQL(), secs(delay), kind, key, token)
}

// execAccounting 统一执行记账语句并取 RowsAffected。
func (q *Queue) execAccounting(ctx context.Context, qSQL string, args ...any) (int64, error) {
	res, err := q.db.ExecContext(ctx, qSQL, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// resetStaleKind 执行 stale 兜底回收（句法见 resetStaleSQL）。
func (q *Queue) resetStaleKind(ctx context.Context, kind string, maxAttempts int) (int64, error) {
	qSQL := q.resetStaleSQL()
	res, err := q.db.ExecContext(ctx, qSQL, maxAttempts-1, kind, secs(q.staleAfter))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// countByStatus 执行 Health 聚合（句法见 countSQL）。
func (q *Queue) countByStatus(ctx context.Context) (Health, error) {
	qSQL := q.countSQL()
	rows, err := q.db.QueryContext(ctx, qSQL)
	if err != nil {
		return Health{}, err
	}
	defer func() { _ = rows.Close() }()
	var h Health
	for rows.Next() {
		var status string
		var n int64
		if err := rows.Scan(&status, &n); err != nil {
			return Health{}, err
		}
		switch status {
		case StatusPending:
			h.Pending = n
		case StatusRunning:
			h.Running = n
		case StatusDeadLetter:
			h.DeadLetter = n
		}
	}
	return h, rows.Err()
}

// tableDDL 建表语句构建器（列宽由 MaxKindLen/MaxKeyLen 派生——护栏与 DDL
// 同源，sqlqueue#85；时间列类型 per 方言；payload 用 MySQL MEDIUMTEXT：
// TEXT 仅 64KB，推送卡/回写载荷超限会在 MySQL strict 下报错、非 strict 下
// 静默截断——SQLite/PG 的 TEXT 无此界）。
func (q *Queue) tableDDL() string {
	ts := map[Dialect]string{Postgres: "TIMESTAMPTZ", MySQL: "DATETIME", SQLite: "REAL"}[q.dialect]
	payloadType := "TEXT"
	if q.dialect == MySQL {
		payloadType = "MEDIUMTEXT"
	}
	return fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
  kind        VARCHAR(%d) NOT NULL,
  dedup_key   VARCHAR(%d) NOT NULL,
  payload     %s NOT NULL,
  status      VARCHAR(16) NOT NULL DEFAULT '%s',
  attempts    INT NOT NULL DEFAULT 0,
  not_before  %s NOT NULL,
  claimed_at  %s,
  claim_token VARCHAR(32),
  created_at  %s NOT NULL,
  PRIMARY KEY (kind, dedup_key)
)`, q.table, MaxKindLen, MaxKeyLen, payloadType, StatusPending, ts, ts, ts)
}

// indexDDL 两个索引语句（认领候选走 (kind, status, not_before)，stale 兜底
// 走 (status, claimed_at)；MySQL 无 CREATE INDEX IF NOT EXISTS，改 ADD INDEX
// + information_schema 判存，见 Migrate）。
func (q *Queue) indexDDL() (createIdx, createStale, idxKind, idxStale string) {
	idxKind = fmt.Sprintf("%s_kind_status", q.table)
	idxStale = fmt.Sprintf("%s_status_claimed", q.table)
	createIdx = fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (kind, status, not_before)`, idxKind, q.table)
	createStale = fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (status, claimed_at)`, idxStale, q.table)
	if q.dialect == MySQL {
		createIdx = fmt.Sprintf(`ALTER TABLE %s ADD INDEX %s (kind, status, not_before)`, q.table, idxKind)
		createStale = fmt.Sprintf(`ALTER TABLE %s ADD INDEX %s (status, claimed_at)`, q.table, idxStale)
	}
	return createIdx, createStale, idxKind, idxStale
}

// Migrate 建 outbox 表与两个索引（幂等；MySQL 无 CREATE INDEX IF NOT
// EXISTS，走 information_schema 判存——queue#87：判存必须带
// table_schema = DATABASE()，否则同实例任意 schema 的同名表+同名索引会让
// 本 schema 的索引永不创建且零报错）。
func (q *Queue) Migrate(ctx context.Context) error {
	createIdx, createStale, idxKind, idxStale := q.indexDDL()
	if _, err := q.db.ExecContext(ctx, q.tableDDL()); err != nil {
		return err
	}
	for _, idx := range []struct{ stmt, name string }{{createIdx, idxKind}, {createStale, idxStale}} {
		if q.dialect == MySQL {
			var n int
			if err := q.db.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?`,
				q.table, idx.name).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				continue // 本 schema 已存在，跳过
			}
		}
		if _, err := q.db.ExecContext(ctx, idx.stmt); err != nil {
			return err
		}
	}
	return nil
}
