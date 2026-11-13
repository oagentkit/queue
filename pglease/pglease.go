// Package pglease — worker.LeaseStore 的 PostgreSQL 实现。
// 多副本对同一 key 竞争时由数据库行级约束保证唯一 winner。
// 走 database/sql 直执行——绕开 ORM 预编译缓存的参数计数问题。
//
// # 两档持有契约（queue#39）
//
// 租约能保证「同一时刻至多一个 winner」，但**不能**保证「winner 不知道自己
// 已经输了」：租约过期被别的副本接管后，旧持有者在下一次 TryAcquire 之前
// 一直以为自己是主。这个窗口是租约语义的固有形态（worker.LeaderElector 头注释
// 已把它限定为「≤一个轮询间隔」），区别只在于**调用方拿什么去容忍它**：
//
//   - **不取 token（TryAcquire，现状语义不变）**：容忍粒度 = 轮询间隔。
//     适合「输一会儿无所谓」的动作——幂等清理、周期性对账、缓存刷新。
//
//   - **取 token（TryAcquireWithToken）**：容忍粒度 = **一次写事务**。
//     业务行上存「最后一次被接受的写的 token」，动作落库带
//     `WHERE ... AND generation <= $token`，易主后旧持有者的写被数据库
//     原子拒绝（影响行数 0），而不是静默覆盖新主的数据。
//
//     判据取 `<=` 而不是 `=`：`=` 只适用于「每个任期重建的业务行」，
//     真实表是长期存活的，新主接手时行上还是旧 token ⇒ `=` 会把**新主的首写**
//     一并拒掉（实测 0 行）。`<=` 让同一持有者反复写、严格更旧的 token 恒被拒，
//     代价是「新主接管后首写之前」旧主的写仍可能落地——那一段由「接管后立即
//     写一次」关闭（pglease_integration_test.go 的成对用例就是这条纪律的样板）。
//
// token 的语义是**单调 fencing token**：同一 holder 续约时**保持不变**
// （它没丢过权，在飞的动作不该被自己作废）；行易主时 **+1**（旧值从此作废，
// 且永不回退）。所以「旧 token 仍然可用」这件事在数据库层是不可能的——
// 这正是它比「动作前再查一次 holder 是不是我」更强的地方：后者的判据
// （holder 名）会被同名副本复用，token 不会。
//
// # token 单调性与行生命周期（queue#89/#90）
//
// 单调性判据是「业务行长期存高水位」，所以 token 必须**跨租约行生命周期**
// 存续——行一旦被删，新行从 1 起步，旧 token 反而成为最高有效 token，
// fencing 的方向性整个反转（拒绝新主、放行旧主）。为此：
//
//   - 走过 token 路径（TryAcquireWithToken）的行打 `fenced` 标记；
//   - Release 对 fenced 行**置墓碑**（holder 清空、expires_at 置过去，
//     generation 保留）而不是删行——让位语义（下一刻即可被夺）不变，
//     token 单调性不破；
//   - Cleanup 只清**非 fenced** 的过期行——fenced 行的 generation 是
//     业务行的 CAS 依据，行在 token 在。fenced 行（每把 fencing 键一行）
//     因此不过期自清，这是换取单调性的显式代价。
//
// 两条获取路径的「易主才 +1」CASE 同款（queue#90）：LeaderElector 走的
// TryAcquire 同样在易主时推高 generation——否则「选举用 elector + 关键写
// 取 token」的文档化组合里，elector 夺走租约的那一刻旧主的在飞写照写不误，
// fencing 恰好在它存在的全部理由（易主）那一刻失效。
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
//
// 构造期 fail-fast（panic，与 sqlqueue.New / worker.NewLeaderElector 同一道
// 纪律，oagentkit/queue#69/#120）：nil db 是装配期语义破坏——nil *sql.DB
// 只会把 panic 推迟到首次用语句的 goroutine（本包是常驻的租约轮询 goroutine，
// 调用方毫无拦截机会），必须在构造期拦截。
func NewPGLeaseStore(db *sql.DB) *PGLeaseStore {
	if db == nil {
		panic("pglease: NewPGLeaseStore db 不能为 nil——nil *sql.DB 只会把 panic 推迟到首次用语句的 goroutine（常在后台的租约轮询里，调用方毫无拦截机会），必须在构造期拦截")
	}
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
//
// ⚠️ 易主时 generation **+1**（queue#90）：本路径是 LeaderElector.tick 的
// 唯一获取路径，而 fencing 的判据是业务行长期存高水位——elector 夺走租约
// 的那一刻若不推高 generation，「选举用 elector + 关键写取 token」的文档化
// 组合里旧主的在飞写恰好照写不误（fencing 在它存在的全部理由那一刻失效）。
// 与 token 路径同款「易主才 +1」：同一 holder 续约保持原值（它没丢过权）。
// 顺带把首次插入的 generation 写成显式 1（此前裸走列 DEFAULT，语义相同）。
func (s *PGLeaseStore) TryAcquire(ctx context.Context, key, holder string, ttl time.Duration) (bool, error) {
	if err := validateIdentity(key, holder); err != nil {
		return false, err
	}
	if err := validateTTL(ttl); err != nil {
		return false, err
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO %[1]s (key, holder, expires_at, generation)
VALUES ($1, $2, now() + make_interval(secs => $3), 1)
ON CONFLICT (key) DO UPDATE SET
  holder     = EXCLUDED.holder,
  expires_at = EXCLUDED.expires_at,
  generation = CASE WHEN %[1]s.holder = EXCLUDED.holder
                    THEN %[1]s.generation
                    ELSE %[1]s.generation + 1 END
WHERE %[1]s.expires_at < now() OR %[1]s.holder = EXCLUDED.holder`, s.table),
		key, holder, ttl.Seconds())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// TryAcquireWithToken 同 TryAcquire，并返回本次持有的 **fencing token**
// （queue#39）。返回的 token 可直接用于动作点的 CAS——业务行存「最后一次被
// 接受的写的 token」，写条件用 `<=`：
//
//	ok, gen, err := store.TryAcquireWithToken(ctx, "k", "replica-a", 15*time.Second)
//	// ... 动作落库时：
//	//   UPDATE biz SET payload = $3
//	//    WHERE id = $1 AND generation <= $2   -- $2 = gen，写成功同时把行推到 gen
//	//
//	// ⚠️ 别写成 `generation = $2`：那要求每个任期重建业务行，否则**新主的首写**
//	// 会被自己拒掉（行上还是旧 token）。接管后先写一次可关闭前后任交叠的窗口。
//
// 拿不到租约（他人持有且未过期）→ (false, 0, nil)，token 无意义。
//
// token 的单调性由 SQL 保证，不是应用侧计数器：
//   - 首次插入 → 1；
//   - 同一 holder 续约 → **保持原值**（它没丢过权，在飞动作不该被自己作废）；
//   - 易主（不同 holder 抢到）→ **+1**，旧值从此作废且永不回退。
//
// 为什么「易主才 +1」而不是「每次获取都 +1」：每次 +1 会让持有者在**正常续约**
// 时作废自己刚排队的写（续约与写并发时，一个极常见的时序），把 fencing 变成
// 「随机丢写」；而「只有易主才变」恰好就是它要表达的语义——权没换，token 就不该换。
//
// ⚠️ 依赖 `generation` 列：旧库必须先跑 Migrate（幂等补列，见下）。
func (s *PGLeaseStore) TryAcquireWithToken(ctx context.Context, key, holder string, ttl time.Duration) (bool, int64, error) {
	if err := validateIdentity(key, holder); err != nil {
		return false, 0, err
	}
	if err := validateTTL(ttl); err != nil {
		return false, 0, err
	}
	var gen int64
	// RETURNING + ON CONFLICT：命中冲突且 WHERE 不成立时**不返回行**，
	// 于是「是否抢到」与「拿到哪个 token」是同一次原子操作的产物——
	// 分两条语句（先判后读）会在两者之间插进另一个写者，读到的可能是别人的 token。
	// fenced 标记（queue#89）：走过本路径的行对其后所有 Release/Cleanup
	// 免删（Release 改墓碑、Cleanup 跳过），generation 由此跨行生命周期存续。
	err := s.db.QueryRowContext(ctx, fmt.Sprintf(`
INSERT INTO %[1]s (key, holder, expires_at, generation, fenced)
VALUES ($1, $2, now() + make_interval(secs => $3), 1, TRUE)
ON CONFLICT (key) DO UPDATE SET
  holder     = EXCLUDED.holder,
  expires_at = EXCLUDED.expires_at,
  fenced     = TRUE,
  generation = CASE WHEN %[1]s.holder = EXCLUDED.holder
                    THEN %[1]s.generation
                    ELSE %[1]s.generation + 1 END
WHERE %[1]s.expires_at < now() OR %[1]s.holder = EXCLUDED.holder
RETURNING generation`, s.table),
		key, holder, ttl.Seconds()).Scan(&gen)
	if errors.Is(err, sql.ErrNoRows) {
		// 他人持有且未过期：没抢到，token 无意义。这不是错误。
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	return true, gen, nil
}

// Release 仅持有者本人可释放（优雅停机让位，加速故障转移）。
// key 或 holder 为空 → 错误：空 holder 的 DELETE 会删掉 holder=” 那把锁，
// 而调用方给不出「本人」的证据（见 validateIdentity）。
//
// fenced 行（queue#89）**置墓碑**而非删行：holder 清空、expires_at 置到
// 过去、generation 保留——下一刻即可被任何副本夺取（让位语义不变），
// 且「清空 holder」让释放者本人随后的再获取也走易主分支 +1（权已让出，
// 释放者的在飞动作该被作废）。直接 DELETE 的话，下一个继任者从 1 起步，
// 而 stale 旧主手里的旧 token 反而是库里的最高有效 token——fencing 的
// 方向性精确反转（真 PostgreSQL 16 实弹复现，queue#89）。两条分支放同一条
// 语句的 CTE 里：同一快照内「删非 fenced / 墓碑 fenced」二选一生效，
// 与并发的 token 获取之间没有「先读后删」的窗口。
func (s *PGLeaseStore) Release(ctx context.Context, key, holder string) error {
	if err := validateIdentity(key, holder); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
WITH del AS (
  DELETE FROM %[1]s WHERE key = $1 AND holder = $2 AND NOT fenced RETURNING 1
), tomb AS (
  UPDATE %[1]s SET holder = '', expires_at = now() - make_interval(secs => 1)
  WHERE key = $1 AND holder = $2 AND fenced RETURNING 1
)
SELECT (SELECT count(*) FROM del) + (SELECT count(*) FROM tomb)`, s.table), key, holder)
	return err
}

// Cleanup 删除已过期的租约行，返回删除行数。
//
// 租约行**只有两种**离开方式：持有者显式 Release（优雅停机），或这里被清掉。
// 缺了后者，进程崩溃/被杀留下的行会永久占着主键（queue#33）——key 是动态生成的
// 场景（临时租约、按任务/租户拼 key）下，表只增不减，查询与索引体积一起退化。
//
// 过期行**不影响正确性**（TryAcquire 的 ON CONFLICT 子句带
// `expires_at < now()`，过期行照样可被夺），所以这不是安全修复而是运维面修复：
// 调用方应周期性调（如随心跳跑，频率取「行数增长可接受」的量级）。
//
// 时钟口径与 TryAcquire 一致——用 DB 时钟 now() 而非应用时钟：清理判据与
// 过期判据必须是同一个钟，否则应用时钟偏慢的副本会把还没过期的行删掉，
// 等于**替对端提前让位**，双主窗口凭空打开。
//
// 与 TryAcquire 的「顺带清理同 key 过期行」不同：那条路径只能清到本次竞争的
// 那一个 key，高 churn 下绝大多数 key 再也不会有第二次竞争，过期行永远等不到
// 它的「顺带」——全局扫描是这类表唯一能收敛的形态。
//
// ⚠️ fenced 行（queue#89）**不清理**：它们的 generation 是业务行 CAS 的
// 高水位，行在 token 在——删掉后新行从 1 起步，stale 旧 token 反而成为
// 最高有效 token（方向性反转）。过期 fenced 行本就可被任何副本夺取
// （expires_at < now() 的夺约判据不受影响），清不清不影响独占性；
// 代价是每把 fencing 键一行常驻，换取 token 单调性。
func (s *PGLeaseStore) Cleanup(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM %s WHERE expires_at < now() AND NOT fenced`, s.table))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}

// Migrate 建租约表，并给**旧表**幂等补上 generation 列。
//
// 两步而不是一步：`CREATE TABLE IF NOT EXISTS` 对已存在的表是 no-op，
// 所以「表早就建好、只是没有 generation 列」的库（本版之前的所有部署）
// 靠它一行都补不上——TryAcquireWithToken 会在运行期报
// `column "generation" does not exist`，而 TryAcquire 照常工作，
// 于是症状表现为「只有想用 fencing 的那条路径坏了」。
// 故补列单独走 `ALTER TABLE ... ADD COLUMN IF NOT EXISTS`（PG 幂等，
// 可反复跑）。`NOT NULL DEFAULT 1` 让存量行直接可读：它们是 fencing 之前
// 的行，首次易主时自然涨到 2。
//
// fenced 列（queue#89）同款幂等补列：标记「走过 token 路径」的行，
// Release/Cleanup 对其免删（墓碑/跳过），generation 由此跨行生命周期存续。
// 存量行缺省 false：只走过 elector 路径的行保持可删可清（queue#33 的
// 收敛语义不变），首次 TryAcquireWithToken 时打标。
func (s *PGLeaseStore) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
  key        VARCHAR(64) PRIMARY KEY,
  holder     VARCHAR(128) NOT NULL,
  expires_at TIMESTAMPTZ  NOT NULL,
  generation BIGINT       NOT NULL DEFAULT 1,
  fenced     BOOLEAN      NOT NULL DEFAULT FALSE
)`, s.table)); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS generation BIGINT NOT NULL DEFAULT 1`, s.table)); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS fenced BOOLEAN NOT NULL DEFAULT FALSE`, s.table))
	return err
}
