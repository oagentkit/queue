package sqlqueue

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// migStubDriver 记录型 stub 驱动（pglease/cleanup_test.go 的 recording driver
// 同手法，queue#85/#87）：把每条语句的 SQL 与实到参数抄下来；Exec 恒成功，
// Query 对 information_schema 判存返回预设的 COUNT 行。连不上真库也能断言
// 「发出去的语句长什么样」——只看「报错了」证不出「报对了错」，也证不出
// 「校验发生在触库之前」。
type migStubDriver struct {
	mu    sync.Mutex
	stmts []migRecStmt
	count int64
}

type migRecStmt struct {
	query string
	args  []driver.Value
}

// migStubSeq 全局注册名序号：sql.Register 同名 panic，用例间不可重名。
var migStubSeq int64

func (d *migStubDriver) Open(string) (driver.Conn, error) { return &migStubConn{d: d}, nil }

func (d *migStubDriver) record(q string, args []driver.NamedValue) {
	vals := make([]driver.Value, len(args))
	for i, a := range args {
		vals[i] = a.Value
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stmts = append(d.stmts, migRecStmt{query: q, args: vals})
}

func (d *migStubDriver) taken() []migRecStmt {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]migRecStmt, len(d.stmts))
	copy(out, d.stmts)
	return out
}

type migStubConn struct{ d *migStubDriver }

func (c *migStubConn) Prepare(query string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c *migStubConn) Close() error                              { return nil }
func (c *migStubConn) Begin() (driver.Tx, error)                 { return nil, driver.ErrSkip }

func (c *migStubConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.d.record(q, args)
	return driver.RowsAffected(0), nil
}

func (c *migStubConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.d.record(q, args)
	if strings.Contains(q, "information_schema.statistics") {
		return &migStubRows{cols: []string{"COUNT(*)"}, vals: [][]driver.Value{{c.d.count}}}, nil
	}
	return nil, io.EOF
}

// migStubRows 单行结果集（判存 COUNT 的应答）。
type migStubRows struct {
	cols []string
	vals [][]driver.Value
	pos  int
}

func (r *migStubRows) Columns() []string { return r.cols }
func (r *migStubRows) Close() error      { return nil }
func (r *migStubRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.vals) {
		return io.EOF
	}
	copy(dest, r.vals[r.pos])
	r.pos++
	return nil
}

// newMigStubDB 挂 stub 驱动的 *sql.DB（不触真库）。
func newMigStubDB(t *testing.T, dialect Dialect, count int64) (*sql.DB, *migStubDriver) {
	t.Helper()
	d := &migStubDriver{count: count}
	name := fmt.Sprintf("sqlqueue_stub_%d", atomic.AddInt64(&migStubSeq, 1))
	sql.Register(name, d)
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, d
}

// newMigStubStore 挂 stub 驱动的 Store。
func newMigStubStore(t *testing.T, dialect Dialect, count int64) (*Store, *migStubDriver) {
	t.Helper()
	db, d := newMigStubDB(t, dialect, count)
	return New(db, dialect, "runner-a"), d
}

func sqDialectName(d Dialect) string {
	switch d {
	case Postgres:
		return "postgres"
	case MySQL:
		return "mysql"
	default:
		return "sqlite"
	}
}

func sqAllDialects() []Dialect { return []Dialect{Postgres, MySQL, SQLite} }
