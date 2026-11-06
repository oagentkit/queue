// Package pglease — worker.LeaseStore 的 PostgreSQL 实现。
// 多副本对同一 key 竞争时由数据库行级约束保证唯一 winner。
// 走 database/sql 直执行——绕开 ORM 预编译缓存的参数计数问题。
package pglease

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"
	"unicode/utf8"
)

// DefaultTable 缺省租约表名。
const DefaultTable = "agentkit_lease"

// 空身份错误（queue#43）。key 与 holder 是租约的两半身份：key 指「哪把锁」，
// holder 指「谁在持」。空值不是「没指定」而是**所有调用方共用同一个身份**——
// 冲突解决子句的「本人持有则顺延」是内容比对（`holder = EXCLUDED.holder`），
// 空串对它恒真 ⇒ 多副本同时自认 leader，而选举语义零报错。
var (
	ErrEmptyKey    = errors.New("pglease: key 不能为空")
	ErrEmptyHolder = errors.New("pglease: holder 不能为空")
	// ErrInvalidTTL 非正 ttl（queue#35）。
	ErrInvalidTTL = errors.New("pglease: ttl 必须 > 0")
	// ErrKeyTooLong / ErrHolderTooLong 超过 Migrate 的列宽上限（queue#34）。
	// 单独成哨兵是为了让调用方能用 errors.Is 判定「这是配置问题不是存储故障」——
	// 超长值到 DB 那层报的是 driver 通用错误，读不出是哪个参数、上限多少。
	ErrKeyTooLong    = errors.New("pglease: key 超长（上限 64 字符）")
	ErrHolderTooLong = errors.New("pglease: holder 超长（上限 128 字符）")
)

// validateIdentity 在拼 SQL 之前拦空身份：校验必须发生在**条件命中之前**，
// 否则空 holder 已经匹配到 holder=” 的行（正是 queue#43 的形态）。
// validateTTL 非正 ttl 必须在触库之前拒绝（queue#35）：租约的 `expires_at`
// 由 `now() + ttl` 算出，ttl ≤ 0 时落库那一刻就已过期——下一次 TryAcquire
// 立刻判「已过期」并可被任何副本抢走，而「同一时刻至多一个 leader」正是这个
// 包存在的理由。JSON/env 配置漏填（零值）是最常见的触发面，那种情况下
// 报一句「ttl 必须 > 0」远比在数据库里看到一堆瞬间过期的行有用。
func validateTTL(ttl time.Duration) error {
	if ttl <= 0 {
		return ErrInvalidTTL
	}
	return nil
}

// 列宽上限，与 Migrate 的 DDL 逐字对应（queue#34）。
//
// ⚠️ PostgreSQL 的 VARCHAR(n) 按**字符**计而非字节，所以判据取 rune 数：
// 逐字节判会把 30 个汉字（30 字符 / 90 字节）误拒成超限——那种错误比不校验
// 更难查，因为它把合法输入拦在了本地。
const (
	MaxKeyLen    = 64
	MaxHolderLen = 128
)

func validateIdentity(key, holder string) error {
	if key == "" {
		return ErrEmptyKey
	}
	if holder == "" {
		return ErrEmptyHolder
	}
	// 长度校验同样必须在触库之前（queue#34）：超长值在 DB 层报的是
	// `value too long for type character varying(64)`，调用方拿不到
	// 「哪个参数超限、上限是多少」——配置写错却表现为存储故障。
	if n := utf8.RuneCountInString(key); n > MaxKeyLen {
		return fmt.Errorf("%w: 实得 %d 字符", ErrKeyTooLong, n)
	}
	if n := utf8.RuneCountInString(holder); n > MaxHolderLen {
		return fmt.Errorf("%w: 实得 %d 字符", ErrHolderTooLong, n)
	}
	return nil
}

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
// key 或 holder 为空 → 错误（见 validateIdentity）。
// expires_at 用 DB 时钟（now() + make_interval）而非应用时钟——写入与过期判定
// 必须同钟：多实例应用时钟漂移会等量放大/缩短租约实际时长，削弱「失联后
// ≤ttl 换主」的承诺（v0.10.29 审计修复）。
func (s *PGLeaseStore) TryAcquire(ctx context.Context, key, holder string, ttl time.Duration) (bool, error) {
	if err := validateIdentity(key, holder); err != nil {
		return false, err
	}
	if err := validateTTL(ttl); err != nil {
		return false, err
	}
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
// key 或 holder 为空 → 错误：空 holder 的 DELETE 会删掉 holder=” 那把锁，
// 而调用方给不出「本人」的证据（见 validateIdentity）。
func (s *PGLeaseStore) Release(ctx context.Context, key, holder string) error {
	if err := validateIdentity(key, holder); err != nil {
		return err
	}
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
