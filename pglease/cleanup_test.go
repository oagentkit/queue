// 外部测试包（pglease_test）：同 pglease_test.go 的理由——worker re-export
// pglease，包内测试再引 worker 会成环。
package pglease_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/oagentkit/queue/pglease"
)

// 记录型 stub driver：把每条 ExecContext 的 SQL 与 args 抄下来，并按预设返回
// RowsAffected。
//
// 为什么不用真 PG：cleanup 的判据是**语句形态 + 返回值透传**，不是 PG 的过期
// 语义（后者由 DSN 门控集成测试盯）。用 stub 才能把「DELETE 判据是不是
// expires_at < now()」钉成判别式断言——退回任何别的判据都会红，而连不上库的
// 手法只能证明「报错了」，证不出「报对了吗」。
type recordingDriver struct {
	mu       sync.Mutex
	stmts    []recordedStmt
	affected int64
}

type recordedStmt struct {
	query string
	args  []driver.Value
}

func (d *recordingDriver) Open(string) (driver.Conn, error) { return &recordingConn{d: d}, nil }

func (d *recordingDriver) queries() []recordedStmt {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]recordedStmt, len(d.stmts))
	copy(out, d.stmts)
	return out
}

type recordingConn struct{ d *recordingDriver }

func (c *recordingConn) Prepare(query string) (driver.Stmt, error) {
	return &recordingStmt{d: c.d, query: query}, nil
}
func (c *recordingConn) Close() error              { return nil }
func (c *recordingConn) Begin() (driver.Tx, error) { return nil, fmt.Errorf("stub: Tx 不支持") }

type recordingStmt struct {
	d     *recordingDriver
	query string
}

func (s *recordingStmt) Close() error  { return nil }
func (s *recordingStmt) NumInput() int { return -1 }
func (s *recordingStmt) Exec(args []driver.Value) (driver.Result, error) {
	s.d.mu.Lock()
	defer s.d.mu.Unlock()
	s.d.stmts = append(s.d.stmts, recordedStmt{query: s.query, args: args})
	return driver.RowsAffected(s.d.affected), nil
}
func (s *recordingStmt) Query([]driver.Value) (driver.Rows, error) {
	return nil, io.EOF
}

// newRecordingStore 每个用例独立注册一个 driver 名（全局注册表不可重名）。
func newRecordingStore(t *testing.T, affected int64) (*pglease.PGLeaseStore, *recordingDriver) {
	t.Helper()
	d := &recordingDriver{affected: affected}
	name := fmt.Sprintf("pglease_stub_%s", strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()))
	sql.Register(name, d)
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return pglease.NewPGLeaseStore(db).WithTable("agentkit_lease_stub"), d
}

// TestCleanupDeletesExpiredRows 过期清理必须存在、按 DB 时钟判定、并透传删除行数
// （queue#33）。
//
// 失效形态：没有 Cleanup ⇒ 进程崩溃留下的行永久占着主键，动态 key 场景下表只增
// 不减。判据三条各自可红：
//   - 判据写 `expires_at <= now()` 或漏掉 WHERE ⇒ 语句里找不到 `expires_at < now()`
//     （漏 WHERE 的 DELETE 会删掉**活着的**租约，等于把独占性拱手让人）；
//   - 判据改用应用时钟（参数化 cutoff）⇒ 语句里出现占位符而不是 now()；
//   - 不透传 RowsAffected ⇒ 调用方拿不到「这次清了多少」，周期任务的观测面归零。
func TestCleanupDeletesExpiredRows(t *testing.T) {
	s, d := newRecordingStore(t, 7)
	n, err := s.Cleanup(context.Background())
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if n != 7 {
		t.Fatalf("删除行数 = %d, want 7——RowsAffected 未透传，周期清理就没有观测面", n)
	}
	stmts := d.queries()
	if len(stmts) != 1 {
		t.Fatalf("Cleanup 应只发一条语句，实得 %d 条: %v", len(stmts), stmts)
	}
	q := stmts[0].query
	if !strings.Contains(q, "agentkit_lease_stub") {
		t.Fatalf("语句未指向配置的表: %q", q)
	}
	if !strings.HasPrefix(strings.TrimSpace(q), "DELETE FROM") {
		t.Fatalf("语句不是 DELETE: %q", q)
	}
	if !strings.Contains(q, "expires_at < now()") {
		t.Fatalf("过期判据必须是 `expires_at < now()`（DB 时钟）——实得 %q。"+
			"漏 WHERE 会删掉活租约，用应用时钟则与 TryAcquire 的过期判定不同钟", q)
	}
	if len(stmts[0].args) != 0 {
		t.Fatalf("DB 时钟判据不该带参数，实得 args=%v", stmts[0].args)
	}
}

// TestCleanupKeepsUnexpiredRows 成对对照：判据不得退化成「清空表」。
//
// 只测「有 DELETE 语句」是测不出判据方向的——`DELETE FROM t`（无 WHERE）同样
// 通过。这里让 stub 报 0 行，断言调用方拿到 0（真实库里「无过期行」正是 0），
// 并把「不得出现无条件 DELETE」钉在语句形态上。
func TestCleanupKeepsUnexpiredRows(t *testing.T) {
	s, d := newRecordingStore(t, 0)
	n, err := s.Cleanup(context.Background())
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if n != 0 {
		t.Fatalf("无过期行时应返回 0，实得 %d", n)
	}
	q := d.queries()[0].query
	if !strings.Contains(q, "WHERE") {
		t.Fatalf("清理语句必须带 WHERE，否则会连活着的租约一起删掉（独占性直接失守）: %q", q)
	}
}
