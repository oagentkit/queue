package sqllease

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// DefaultTable 缺省租约表名。
const DefaultTable = "agentkit_lease"

// 空身份错误（queue#43）。key 与 holder 是租约的两半身份：key 指「哪把锁」，
// holder 指「谁在持」。空值不是「没指定」而是**所有调用方共用同一个身份**——
// 条件 UPDATE 的「本人持有则顺延」是内容比对（`holder = ?`），空串对它恒真
// ⇒ 多副本同时自认 leader，而选举语义零报错。
var (
	ErrEmptyKey    = errors.New("sqllease: key 不能为空")
	ErrEmptyHolder = errors.New("sqllease: holder 不能为空")
	// ErrInvalidTTL 非正 ttl（queue#35）。
	ErrInvalidTTL = errors.New("sqllease: ttl 必须 > 0")
)

// validateIdentity 在拼 SQL 之前拦空身份：校验必须发生在**条件命中之前**，
// 否则空 holder 已经匹配到 `holder = ”` 的行（正是 queue#43 的形态）。
// validateTTL 非正 ttl 必须在触库之前拒绝（queue#35）：`expires_at` 由 ttl 直接
// 算出，ttl ≤ 0 时租约**落库即过期**——下一个 TryAcquire 立刻判「已过期」并可被
// 任何副本抢走，击穿「同一时刻至多一个 leader」红线，而且看起来一切正常
// （选举每次都「成功」，只是从来不是独占）。配置漏填是最常见的触发面。
func validateTTL(ttl time.Duration) error {
	if ttl <= 0 {
		return ErrInvalidTTL
	}
	return nil
}

func validateIdentity(key, holder string) error {
	if key == "" {
		return ErrEmptyKey
	}
	if holder == "" {
		return ErrEmptyHolder
	}
	return nil
}

// tableIdentRe 表名安全白名单：合法 SQL 标识符（防拼接注入，pglease 同款）。
var tableIdentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Dialect SQL 方言。
type Dialect int

const (
	// Postgres PostgreSQL（占位符 $N，now() + make_interval）。
	Postgres Dialect = iota
	// MySQL MySQL / MariaDB（占位符 ?，NOW() + INTERVAL）。
	MySQL
	// SQLite SQLite（占位符 ?，strftime('%s','now') unixepoch REAL）。
	SQLite
)

// Store worker.LeaseStore 的 SQL 参考实现（三方言）。
type Store struct {
	db      *sql.DB
	dialect Dialect
	table   string
}

// New 构造（表名用 DefaultTable；WithTable 覆盖）。
func New(db *sql.DB, dialect Dialect) *Store {
	return &Store{db: db, dialect: dialect, table: DefaultTable}
}

// WithTable 指定租约表名（兼容已有库的自定义表名）。
// 非空且不匹配标识符白名单时保持原表名（后续 SQL 仍用安全值）。
func (s *Store) WithTable(name string) *Store {
	if name != "" && tableIdentRe.MatchString(name) {
		s.table = name
	}
	return s
}

// ident 按方言引用标识符。
//
// MySQL 的反引号不只是风格：`key` 是 MySQL **保留字**，裸用在建表
// （`key VARCHAR(64) PRIMARY KEY`）与 UPDATE/DELETE（`WHERE key = ?`）里
// 直接语法错误——`sqllease` 的 MySQL 分支因此整条不可用，而三方言测试矩阵
// 只覆盖 SQLite（SQLite 不把 key 当保留字，裸用照样跑），缺陷一路静默
// （oagentkit/queue#32）。Postgres/SQLite 用标准双引号；SQLite 两种都接受，
// 标准形态优先。
func (s *Store) ident(name string) string {
	if s.dialect == MySQL {
		return "`" + name + "`"
	}
	return `"` + name + `"`
}

// ph 方言占位符（PG 用 $N，其余 ?）。
func (s *Store) ph(i int) string {
	if s.dialect == Postgres {
		return fmt.Sprintf("$%d", i)
	}
	return "?"
}

// nowPlus / nowCmp DB 时钟表达式（写入侧 / 判定侧——两处必须同钟）。
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

func (s *Store) nowCmp() string {
	col := s.ident("expires_at")
	switch s.dialect {
	case Postgres:
		return col + " < now()"
	case MySQL:
		return col + " < NOW()"
	default:
		return col + " < strftime('%s','now')"
	}
}

// TryAcquire 原子获取或续约租约（worker.LeaseStore 实现）。
// 空闲/已过期/持有者就是 holder → true；他人持有未过期 → false。
// key 或 holder 为空 → 错误（见 validateIdentity）。
func (s *Store) TryAcquire(ctx context.Context, key, holder string, ttl time.Duration) (bool, error) {
	if err := validateIdentity(key, holder); err != nil {
		return false, err
	}
	if err := validateTTL(ttl); err != nil {
		return false, err
	}
	n, err := s.conditionalAcquire(ctx, key, holder, ttl)
	if err != nil {
		return false, err
	}
	if n > 0 {
		return true, nil
	}
	// 无行 → INSERT 建租约。多副本竞态下他实例抢先 INSERT 是预期路径：
	// 冲突错误不外露（无 ORM logger 即无 duplicate-key 红字刷屏——argus
	// 双副本实弹当天的降噪诉求，oagentkit/queue#24），静默回退再试一次
	// 条件 UPDATE（对手刚插入 → 现在走条件判定）。
	if err := s.insert(ctx, key, holder, ttl); err != nil {
		n, err2 := s.conditionalAcquire(ctx, key, holder, ttl)
		if err2 != nil {
			return false, err // 原始 INSERT 错误更有诊断价值
		}
		return n > 0, nil
	}
	return true, nil
}

// conditionalAcquire 条件 UPDATE：WHERE 挂「本人持有或已过期」，命中即占用
// 或顺延。单语句完成「判-占-续」，无查后写竞窗。
func (s *Store) conditionalAcquire(ctx context.Context, key, holder string, ttl time.Duration) (int64, error) {
	q := fmt.Sprintf(`UPDATE %s SET %s = %s, %s = %s
WHERE %s = %s AND (%s = %s OR %s)`,
		s.ident(s.table), s.ident("holder"), s.ph(1), s.ident("expires_at"), s.nowPlus(s.ph(2)),
		s.ident("key"), s.ph(3), s.ident("holder"), s.ph(4), s.nowCmp())
	res, err := s.db.ExecContext(ctx, q, holder, ttl.Seconds(), key, holder)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) insert(ctx context.Context, key, holder string, ttl time.Duration) error {
	q := fmt.Sprintf(`INSERT INTO %s (%s, %s, %s) VALUES (%s, %s, %s)`,
		s.ident(s.table), s.ident("key"), s.ident("holder"), s.ident("expires_at"),
		s.ph(1), s.ph(2), s.nowPlus(s.ph(3)))
	_, err := s.db.ExecContext(ctx, q, key, holder, ttl.Seconds())
	return err
}

// Release 仅持有者本人可释放（优雅停机让位）；非持有者/key 不存在 no-op。
// key 或 holder 为空 → 错误：空 holder 的 DELETE 会删掉 `holder = ”` 那把锁，
// 而调用方给不出「本人」的证据（见 validateIdentity）。
func (s *Store) Release(ctx context.Context, key, holder string) error {
	if err := validateIdentity(key, holder); err != nil {
		return err
	}
	q := fmt.Sprintf(`DELETE FROM %s WHERE %s = %s AND %s = %s`,
		s.ident(s.table), s.ident("key"), s.ph(1), s.ident("holder"), s.ph(2))
	_, err := s.db.ExecContext(ctx, q, key, holder)
	return err
}

// Migrate 建租约表（幂等）。
func (s *Store) Migrate(ctx context.Context) error {
	typ := map[Dialect]string{
		Postgres: "TIMESTAMPTZ",
		MySQL:    "DATETIME",
		SQLite:   "REAL",
	}[s.dialect]
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
  %s VARCHAR(64) PRIMARY KEY,
  %s VARCHAR(128) NOT NULL,
  %s %s NOT NULL
)`, s.ident(s.table), s.ident("key"), s.ident("holder"), s.ident("expires_at"), typ))
	return err
}
