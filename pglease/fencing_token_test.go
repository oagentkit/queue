// 外部测试包（pglease_test）：同 cleanup_test.go 的理由——worker re-export
// pglease，包内测试再引 worker 会成环。
package pglease_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oagentkit/queue/pglease"
)

// fencingDriver 记录语句并按预设返回「一行 generation」或「零行」。
//
// 为什么用 stub 而不是真 PG：本组用例要钉的是**语句形态**（token 是不是与
// 获取同一条语句产出、易主与续约的 CASE 各走哪支、校验是否先于触库），
// 这些在真库上只能间接观察；而 DSN 门控的集成用例已经覆盖真实语义
// （fencing_token_integration_test.go）。
type fencingDriver struct {
	mu    sync.Mutex
	stmts []recordedStmt
	gen   int64
	found bool
}

func (d *fencingDriver) Open(string) (driver.Conn, error) { return &fencingConn{d: d}, nil }

func (d *fencingDriver) record(q string, args []driver.Value) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stmts = append(d.stmts, recordedStmt{query: q, args: args})
}

func (d *fencingDriver) recorded() []recordedStmt {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]recordedStmt, len(d.stmts))
	copy(out, d.stmts)
	return out
}

type fencingConn struct{ d *fencingDriver }

func (c *fencingConn) Prepare(query string) (driver.Stmt, error) {
	return &fencingStmt{d: c.d, query: query}, nil
}
func (c *fencingConn) Close() error              { return nil }
func (c *fencingConn) Begin() (driver.Tx, error) { return nil, fmt.Errorf("stub: Tx 不支持") }

type fencingStmt struct {
	d     *fencingDriver
	query string
}

func (s *fencingStmt) Close() error  { return nil }
func (s *fencingStmt) NumInput() int { return -1 }

func (s *fencingStmt) Exec(args []driver.Value) (driver.Result, error) {
	s.d.record(s.query, args)
	return driver.RowsAffected(0), nil
}

func (s *fencingStmt) Query(args []driver.Value) (driver.Rows, error) {
	s.d.record(s.query, args)
	if !s.d.found {
		// 零行：ON CONFLICT 命中但 WHERE 不成立 —— 「没抢到」的那条路径。
		return &fencingRows{}, nil
	}
	return &fencingRows{vals: []driver.Value{s.d.gen}}, nil
}

type fencingRows struct {
	vals []driver.Value
	done bool
}

func (r *fencingRows) Columns() []string { return []string{"generation"} }
func (r *fencingRows) Close() error      { return nil }
func (r *fencingRows) Next(dest []driver.Value) error {
	if r.done || r.vals == nil {
		return io.EOF
	}
	r.done = true
	copy(dest, r.vals)
	return nil
}

func newFencingStore(t *testing.T, gen int64, found bool) (*pglease.PGLeaseStore, *fencingDriver) {
	t.Helper()
	d := &fencingDriver{gen: gen, found: found}
	name := fmt.Sprintf("pglease_fencing_%s", strings.NewReplacer("/", "_", " ", "_", "=", "_").Replace(t.Name()))
	sql.Register(name, d)
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return pglease.NewPGLeaseStore(db).WithTable("agentkit_lease_fence"), d
}

// segmentBetween 取 s 中 [from, to) 之间的一段，缺任一标记即 fail。
func segmentBetween(t *testing.T, s, from, to string) string {
	t.Helper()
	i := strings.Index(s, from)
	if i < 0 {
		t.Fatalf("语句里找不到 %q，无从判定分支结构: %q", from, s)
	}
	rest := s[i+len(from):]
	j := strings.Index(rest, to)
	if j < 0 {
		t.Fatalf("语句里找不到 %q，无从判定分支结构: %q", to, s)
	}
	return rest[:j]
}

// TestTryAcquireWithTokenReturnsGenerationFromSameStatement token 必须与
// 「是否抢到」由**同一条语句**产出（queue#39）。
//
// 失效形态：先 `SELECT generation` 再 `UPDATE ... RETURNING`（或反之）——
// 两条语句之间另一个写者可以插进来，于是调用方拿到的是**别人的** token，
// 却以为自己持有它。那比没有 token 更坏：它给了一个看起来可信的 CAS 依据。
func TestTryAcquireWithTokenReturnsGenerationFromSameStatement(t *testing.T) {
	s, d := newFencingStore(t, 7, true)
	ok, gen, err := s.TryAcquireWithToken(context.Background(), "k", "replica-a", 15*time.Second)
	if err != nil {
		t.Fatalf("TryAcquireWithToken: %v", err)
	}
	if !ok {
		t.Fatalf("抢到租约却返回 ok=false")
	}
	if gen != 7 {
		t.Fatalf("token = %d, want 7——RETURNING 的 generation 未透传", gen)
	}
	stmts := d.recorded()
	if len(stmts) != 1 {
		t.Fatalf("获取与取 token 必须是同一条语句，实得 %d 条: %v", len(stmts), stmts)
	}
	q := stmts[0].query
	if !strings.Contains(q, "RETURNING generation") {
		t.Fatalf("语句缺 RETURNING generation——token 只能由这条原子语句产出: %q", q)
	}
	if !strings.Contains(q, "INSERT INTO agentkit_lease_fence") {
		t.Fatalf("语句未指向配置的表: %q", q)
	}
	if !strings.Contains(q, "ON CONFLICT (key) DO UPDATE") {
		t.Fatalf("必须是 UPSERT：拆成先查后写即失去原子性: %q", q)
	}
	if len(stmts[0].args) != 3 {
		t.Fatalf("参数应为 (key, holder, ttl 秒)，实得 %v", stmts[0].args)
	}
}

// TestTryAcquireWithTokenIncrementsOnlyOnTakeover 单调性必须按「权换没换」分：
// 同一 holder 续约**保持**，易主才 **+1**。
//
// 两支各自对应一种真实错实现：
//   - 续约也 +1 ⇒ 持有者在正常续约时作废自己刚排队的写（fencing 退化成随机丢写）；
//   - 易主不 +1 ⇒ token 从不变化，CAS 永远成立，fencing 形同虚设。
//
// 所以断言不是「语句里有 + 1」（那样第一支的错实现照样过），而是**分支结构**：
// THEN 支（同 holder）取原值且不含 +1，ELSE 支（异 holder）才 +1。
func TestTryAcquireWithTokenIncrementsOnlyOnTakeover(t *testing.T) {
	s, d := newFencingStore(t, 3, true)
	if _, _, err := s.TryAcquireWithToken(context.Background(), "k", "replica-a", time.Second); err != nil {
		t.Fatalf("TryAcquireWithToken: %v", err)
	}
	q := d.recorded()[0].query

	cond := segmentBetween(t, q, "CASE WHEN", "THEN")
	if !strings.Contains(cond, "holder") || !strings.Contains(cond, "EXCLUDED.holder") {
		t.Fatalf("分支条件必须是「holder 换没换」: %q", cond)
	}
	thenBranch := segmentBetween(t, q, "THEN", "ELSE")
	if !strings.Contains(thenBranch, "generation") {
		t.Fatalf("续约支必须取原 generation: %q", thenBranch)
	}
	if strings.Contains(thenBranch, "+ 1") {
		t.Fatalf("续约支不得 +1——那会让持有者作废自己刚排队的写: %q", thenBranch)
	}
	elseBranch := segmentBetween(t, q, "ELSE", "END")
	if !strings.Contains(elseBranch, "+ 1") {
		t.Fatalf("易主支必须 +1，否则 token 从不变化、CAS 永远成立: %q", elseBranch)
	}

	// 新行起点：首次插入即 1（与「易主才涨」同一把尺子——首次获取也是第一次持权）。
	if !strings.Contains(q, "VALUES ($1, $2, now() + make_interval(secs => $3), 1)") {
		t.Fatalf("首次插入的 generation 应为字面 1（不是 0、也不是 +1 表达式）: %q", q)
	}
}

// TestTryAcquireWithTokenNotAcquiredIsNotError 没抢到不是错误，且 token 无意义。
//
// 失效形态：把 `sql.ErrNoRows` 当错误往上抛 ⇒ 每个「竞争失败」的正常副本
// 都会在日志里刷一条错误，真故障被淹掉；反过来返回 (false, 非零) 会让调用方
// 拿着别人的 token 去写。
func TestTryAcquireWithTokenNotAcquiredIsNotError(t *testing.T) {
	s, _ := newFencingStore(t, 0, false)
	ok, gen, err := s.TryAcquireWithToken(context.Background(), "k", "replica-a", time.Second)
	if err != nil {
		t.Fatalf("没抢到不该是错误，实得: %v", err)
	}
	if ok {
		t.Fatalf("零行却返回 ok=true")
	}
	if gen != 0 {
		t.Fatalf("没抢到时 token 必须是 0（无意义值），实得 %d——非零会被误用成 CAS 依据", gen)
	}
}

// TestTryAcquireWithTokenValidatesBeforeTouchingDB 校验必须发生在触库之前。
//
// 判据取**语句条数 = 0**：只断言「返回了某个错误」无法区分「本地拦下」与
// 「发到库里被拒」——后者在空 holder 场景恰好是 queue#43 那种双主形态，
// 而超长 key 场景会把配置错误伪装成存储故障（queue#34）。
func TestTryAcquireWithTokenValidatesBeforeTouchingDB(t *testing.T) {
	cases := []struct {
		name   string
		key    string
		holder string
		ttl    time.Duration
		want   error
	}{
		{"空 key", "", "h", time.Second, pglease.ErrEmptyKey},
		{"空 holder", "k", "", time.Second, pglease.ErrEmptyHolder},
		{"非正 ttl", "k", "h", 0, pglease.ErrInvalidTTL},
		{"超长 key", strings.Repeat("k", pglease.MaxKeyLen+1), "h", time.Second, pglease.ErrKeyTooLong},
		{"超长 holder", "k", strings.Repeat("h", pglease.MaxHolderLen+1), time.Second, pglease.ErrHolderTooLong},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, d := newFencingStore(t, 1, true)
			if _, _, err := s.TryAcquireWithToken(context.Background(), c.key, c.holder, c.ttl); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if n := len(d.recorded()); n != 0 {
				t.Fatalf("校验必须先于触库，实际发出了 %d 条语句: %v", n, d.recorded())
			}
		})
	}
}

// TestMigrateAddsGenerationColumnIdempotently Migrate 必须给**旧表**补列。
//
// 失效形态：只在 CREATE TABLE 里写 generation。对已存在的表
// `CREATE TABLE IF NOT EXISTS` 是 no-op ⇒ 存量部署补不上列，
// TryAcquireWithToken 在运行期报 `column "generation" does not exist`，
// 而 TryAcquire 照常工作——症状是「只有想用 fencing 的那条路径坏了」。
func TestMigrateAddsGenerationColumnIdempotently(t *testing.T) {
	s, d := newFencingStore(t, 0, false)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	stmts := d.recorded()
	if len(stmts) < 2 {
		t.Fatalf("Migrate 应发两条语句（建表 + 补列），实得 %d 条: %v", len(stmts), stmts)
	}
	var alter string
	for _, st := range stmts {
		if strings.HasPrefix(strings.TrimSpace(st.query), "ALTER TABLE") {
			alter = st.query
		}
	}
	if alter == "" {
		t.Fatalf("Migrate 未发 ALTER TABLE：存量表补不上 generation 列: %v", stmts)
	}
	if !strings.Contains(alter, "ADD COLUMN IF NOT EXISTS generation") {
		t.Fatalf("补列必须是 `ADD COLUMN IF NOT EXISTS`（幂等，可反复跑）: %q", alter)
	}
	if !strings.Contains(alter, "NOT NULL DEFAULT 1") {
		t.Fatalf("补列需 `NOT NULL DEFAULT 1`，否则存量行读出 NULL、Scan 直接失败: %q", alter)
	}
	if !strings.Contains(stmts[0].query, "generation BIGINT") {
		t.Fatalf("新库建表也应直接带 generation 列（省一条 ALTER）: %q", stmts[0].query)
	}
}
