package sqllease

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubArgsDriver 记录型 stub 驱动（pglease/cleanup_test.go 的 recording driver
// 同手法，queue#83/#84）：把每条语句的 SQL 与实到参数抄下来，Exec 按预设返回
// RowsAffected。连不上真库也能断言「发出去的语句长什么样」——只看「报错了」
// 证不出「报对了错」。
type stubArgsDriver struct {
	mu       sync.Mutex
	stmts    []stubRecStmt
	affected int64
}

type stubRecStmt struct {
	query string
	args  []driver.Value
}

// stubSeq 全局注册名序号：sql.Register 同名 panic，用例间不可重名。
var stubSeq int64

func (d *stubArgsDriver) Open(string) (driver.Conn, error) { return &stubArgsConn{d: d}, nil }

func (d *stubArgsDriver) record(q string, args []driver.NamedValue) {
	vals := make([]driver.Value, len(args))
	for i, a := range args {
		vals[i] = a.Value
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stmts = append(d.stmts, stubRecStmt{query: q, args: vals})
}

func (d *stubArgsDriver) taken() []stubRecStmt {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]stubRecStmt, len(d.stmts))
	copy(out, d.stmts)
	return out
}

type stubArgsConn struct{ d *stubArgsDriver }

func (c *stubArgsConn) Prepare(query string) (driver.Stmt, error) {
	return nil, driver.ErrSkip
}
func (c *stubArgsConn) Close() error              { return nil }
func (c *stubArgsConn) Begin() (driver.Tx, error) { return nil, driver.ErrSkip }

func (c *stubArgsConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.d.record(q, args)
	return driver.RowsAffected(c.d.affected), nil
}

func (c *stubArgsConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.d.record(q, args)
	return nil, io.EOF
}

// newStubStore 挂 stub 驱动的 Store（不经真库；表名用缺省）。
func newStubStore(t *testing.T, dialect Dialect, affected int64) (*Store, *stubArgsDriver) {
	t.Helper()
	d := &stubArgsDriver{affected: affected}
	name := fmt.Sprintf("sqllease_stubargs_%d", atomic.AddInt64(&stubSeq, 1))
	sql.Register(name, d)
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db, dialect), d
}

func dialectName(d Dialect) string {
	switch d {
	case Postgres:
		return "postgres"
	case MySQL:
		return "mysql"
	default:
		return "sqlite"
	}
}

func allDialects() []Dialect { return []Dialect{Postgres, MySQL, SQLite} }

// guardDeadline 超时兜底：stub 驱动不触网，用不到这个时限——带上只为防用例
// 自身改坏后挂死。
func guardDeadline() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 3*time.Second)
}
