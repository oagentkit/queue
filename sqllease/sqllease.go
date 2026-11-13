package sqllease

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
// 条件 UPDATE 的「本人持有则顺延」是内容比对（`holder = ?`），空串对它恒真
// ⇒ 多副本同时自认 leader，而选举语义零报错。
var (
	ErrEmptyKey    = errors.New("sqllease: key 不能为空")
	ErrEmptyHolder = errors.New("sqllease: holder 不能为空")
	// ErrInvalidTTL 非正 ttl（queue#35）。
	ErrInvalidTTL = errors.New("sqllease: ttl 必须 > 0")
	// ErrKeyTooLong / ErrHolderTooLong 超过 Migrate 的列宽上限（queue#83，
	// pglease#34 的三方言移植）。单独成哨兵是为了让调用方能用 errors.Is 判定
	// 「这是配置问题不是存储故障」——超长值到 DB 那层报的是 driver 通用错误
	// （PG：value too long；MySQL strict：Data too long），读不出是哪个参数、
	// 上限多少；MySQL 非 strict 更糟：**静默截断**到 64 字符，前 64 字符相同的
	// 两个 key 塌成同一把锁（假互斥/假独占，全程零报错）；SQLite 干脆忽略
	// VARCHAR 宽度原样落库。三方言各自失效形态不同，但「配置错误必须表现为
	// 配置错误」这条与方言无关。
	ErrKeyTooLong    = errors.New("sqllease: key 超长（上限 64 字符）")
	ErrHolderTooLong = errors.New("sqllease: holder 超长（上限 128 字符）")
)

// 列宽上限，与 Migrate 的 DDL 由同一组常量派生（queue#83，pglease#34 的
// 三方言移植）——两处各写一个数字、改一处忘另一处，本缺陷就会复现。
//
// ⚠️ SQL 的 VARCHAR(n) 按**字符**计而非字节，所以判据取 rune 数：逐字节判
// 会把 64 个汉字（64 字符 / 192 字节）误拒成超限——那种错误比不校验更难查，
// 因为它把合法输入拦在了本地（pglease.TestLengthGuardCountsRunesNotBytes 是
// 这条选择的判别点样板）。
const (
	MaxKeyLen    = 64
	MaxHolderLen = 128
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
	// 长度校验同样必须在触库之前（queue#83）：超长值在三方言下的结局各不相同
	// （PG/MySQL strict 报 driver 通用错误、MySQL 非 strict 静默截断塌行、
	// SQLite 原样放行），没有一种会把「key 拼了长前缀、holder 用了整条 URI」
	// 这类配置错误读出来。Release 与 TryAcquire 两个入口共用本校验——Release
	// 的超长 holder DELETE 删不到自己那把锁，让位会静默失败，同样必须拦。
	if n := utf8.RuneCountInString(key); n > MaxKeyLen {
		return fmt.Errorf("%w: 实得 %d 字符", ErrKeyTooLong, n)
	}
	if n := utf8.RuneCountInString(holder); n > MaxHolderLen {
		return fmt.Errorf("%w: 实得 %d 字符", ErrHolderTooLong, n)
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
//
// 构造期 fail-fast（panic，与 sqlqueue.New / worker.NewLeaderElector 同一道
// 纪律，oagentkit/queue#69/#120）：nil db 是装配期语义破坏——nil *sql.DB
// 只会把 panic 推迟到首次用语句的 goroutine（本包是常驻的租约轮询 goroutine，
// 调用方毫无拦截机会），必须在构造期拦截。
func New(db *sql.DB, dialect Dialect) *Store {
	if db == nil {
		panic("sqllease: New db 不能为 nil——nil *sql.DB 只会把 panic 推迟到首次用语句的 goroutine（常在后台的租约轮询里，调用方毫无拦截机会），必须在构造期拦截")
	}
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

// nowPlus DB 时钟表达式（写入侧——与 ExpiredPredicate 的判定侧必须同钟）。
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

// ExpiredPredicate 返回本方言的「租约已过期」判定表达式（queue#84）：
// Postgres "expires_at" < now() / MySQL `expires_at` < NOW() /
// SQLite "expires_at" < strftime('%s','now')。
//
// 这是方言时钟判据的**公共出口**：TryAcquire 的可夺判定与 Cleanup 的清理
// 判据都由它单源派生（写入侧 nowPlus 与判定侧同钟，oagentkit/queue#26 红线）。
// 调用方做 Cleanup 全局 DELETE 覆盖不到的定向清理（按前缀/租户/业务域删除
// 过期行）时，把它拼进自己的 WHERE 即可与库内判定同钟：
//
//	pred := store.ExpiredPredicate()
//	q := "DELETE FROM " + tbl + " WHERE " + keyCol + " LIKE ? AND " + pred
//
// ⚠️ 不要自己写 `expires_at < ?` 绑应用时钟的 cutoff——那是另一种钟：应用
// 时钟偏慢的副本会删掉还没过期的行，等于替对端提前让位，双主窗口凭空打开。
// 判定必须是 `<`（严格小于）：`<=` 会把恰在边界上活着的租约一并删掉。
//
// 列名按方言引用（MySQL 反引号——key 保留字同款纪律，queue#32），返回值
// 只含判据、不含表名与占位符，可直接 AND 进既有 WHERE。
func (s *Store) ExpiredPredicate() string {
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
// 或顺延。单语句完成「判-占-续」，无查后写竞窗。过期判据与 Cleanup 同源
// （ExpiredPredicate，queue#84）。
func (s *Store) conditionalAcquire(ctx context.Context, key, holder string, ttl time.Duration) (int64, error) {
	q := fmt.Sprintf(`UPDATE %s SET %s = %s, %s = %s
WHERE %s = %s AND (%s = %s OR %s)`,
		s.ident(s.table), s.ident("holder"), s.ph(1), s.ident("expires_at"), s.nowPlus(s.ph(2)),
		s.ident("key"), s.ph(3), s.ident("holder"), s.ph(4), s.ExpiredPredicate())
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

// Cleanup 删除已过期的租约行，返回删除行数（queue#84，pglease#33 的三方言
// 移植）。
//
// 租约行**只有两种**离开方式：持有者显式 Release（优雅停机），或这里被清掉。
// 缺了后者，进程崩溃/被杀留下的行会永久占着主键（queue#33）——key 是动态生成的
// 场景（临时租约、按任务/租户拼 key）下，表只增不减，查询与索引体积一起退化。
//
// 过期行**不影响正确性**（TryAcquire 的条件 UPDATE 带 ExpiredPredicate 判据，
// 过期行照样可被夺），所以这不是安全修复而是运维面修复：调用方应周期性调
// （如随心跳跑，频率取「行数增长可接受」的量级）。
//
// 时钟口径与 TryAcquire 一致——判据由 ExpiredPredicate 单源派生（DB 时钟，
// 三方言各 NOW()/strftime 形态）：清理判据与过期判据必须是同一个钟，否则应用
// 时钟偏慢的副本会把还没过期的行删掉，等于**替对端提前让位**，双主窗口凭空
// 打开。这也是本方法不提供「按应用时钟传 cutoff」重载的原因。
//
// 只想定向清理（按前缀/租户）的调用方：把 ExpiredPredicate() 拼进自己的
// DELETE WHERE，不要自己绑应用时钟（见其 doc）。与 TryAcquire 的「顺带清理
// 同 key 过期行」不同：那条路径只能清到本次竞争的那一个 key，高 churn 下
// 绝大多数 key 再也不会有第二次竞争——全局扫描是这类表唯一能收敛的形态。
func (s *Store) Cleanup(ctx context.Context) (int64, error) {
	q := fmt.Sprintf(`DELETE FROM %s WHERE %s`, s.ident(s.table), s.ExpiredPredicate())
	res, err := s.db.ExecContext(ctx, q)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}

// Migrate 建租约表（幂等）。列宽由 MaxKeyLen/MaxHolderLen 派生（queue#83）：
// 护栏与 DDL 同源，改上限不必记得改两处。
func (s *Store) Migrate(ctx context.Context) error {
	typ := map[Dialect]string{
		Postgres: "TIMESTAMPTZ",
		MySQL:    "DATETIME",
		SQLite:   "REAL",
	}[s.dialect]
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
  %s VARCHAR(%d) PRIMARY KEY,
  %s VARCHAR(%d) NOT NULL,
  %s %s NOT NULL
)`, s.ident(s.table), s.ident("key"), MaxKeyLen, s.ident("holder"), MaxHolderLen,
		s.ident("expires_at"), typ))
	return err
}
