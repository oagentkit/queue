// Package pglease — worker.LeaseStore 的 PostgreSQL 实现。
// 多副本对同一 key 竞争时由数据库行级约束保证唯一 winner。
// 走 database/sql 直执行——绕开 ORM 预编译缓存的参数计数问题。
package pglease

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"time"
)

// DefaultTable 缺省租约表名。
const DefaultTable = "agentkit_lease"

// tableIdentRe 表名安全白名单：合法 SQL 标识符（防拼接注入）。
var tableIdentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// PGLeaseStore 实现 worker.LeaseStore。
type PGLeaseStore struct {
	db    *sql.DB
	table string
}

// NewPGLeaseStore 构造（表名用 DefaultTable；用 WithTable 覆盖）。
func NewPGLeaseStore(db *sql.DB) *PGLeaseStore {
	return &PGLeaseStore{db: db, table: DefaultTable}
}

// WithTable 指定租约表名（兼容已有库的自定义表名）。
// 非空且不匹配标识符白名单时保持原表名（后续 SQL 仍用安全值）。
func (s *PGLeaseStore) WithTable(name string) *PGLeaseStore {
	if name != "" && tableIdentRe.MatchString(name) {
		s.table = name
	}
	return s
}

// TryAcquire 原子获取或续约：空闲/已过期/本人持有 → true。
// expires_at 用 DB 时钟（now() + make_interval）而非应用时钟——写入与过期判定
// 必须同钟：多实例应用时钟漂移会等量放大/缩短租约实际时长，削弱「失联后
// ≤ttl 换主」的承诺（v0.10.29 审计修复）。
func (s *PGLeaseStore) TryAcquire(ctx context.Context, key, holder string, ttl time.Duration) (bool, error) {
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO %s (key, holder, expires_at)
VALUES ($1, $2, now() + make_interval(secs => $3))
ON CONFLICT (key) DO UPDATE SET holder = EXCLUDED.holder, expires_at = EXCLUDED.expires_at
WHERE %s.expires_at < now() OR %s.holder = EXCLUDED.holder`, s.table, s.table, s.table),
		key, holder, ttl.Seconds())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Release 仅持有者本人可释放（优雅停机让位，加速故障转移）。
func (s *PGLeaseStore) Release(ctx context.Context, key, holder string) error {
	_, err := s.db.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM %s WHERE key = $1 AND holder = $2`, s.table), key, holder)
	return err
}

// Migrate 建租约表。
func (s *PGLeaseStore) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
  key        VARCHAR(64) PRIMARY KEY,
  holder     VARCHAR(128) NOT NULL,
  expires_at TIMESTAMPTZ  NOT NULL
)`, s.table))
	return err
}
