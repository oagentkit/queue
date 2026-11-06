package sqllease

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// recDriver 记录收到 SQL 的假驱动：MySQL 分支没有真库可连，但「发出去的
// 语句长什么样」在 database/sql 驱动层就能看到——`key` 是不是被引用，是
// 语法正确性的直接证据（queue#32）。
type recDriver struct {
	mu    sync.Mutex
	stmts []string
}

func (d *recDriver) record(q string) {
	d.mu.Lock()
	d.stmts = append(d.stmts, q)
	d.mu.Unlock()
}

func (d *recDriver) taken() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, len(d.stmts))
	copy(out, d.stmts)
	return out
}

func (d *recDriver) Open(string) (driver.Conn, error) { return &recConn{d: d}, nil }

type recConn struct{ d *recDriver }

func (c *recConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c *recConn) Close() error                        { return nil }
func (c *recConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }

func (c *recConn) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	c.d.record(q)
	return driver.RowsAffected(1), nil
}

// quotedSeg 引号包住的片段（反引号或双引号）；identRe 裸标识符词元。
var (
	quotedSeg = regexp.MustCompile("`[^`]*`|\"[^\"]*\"")
	identRe   = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
)

// bareIdent 报告语句里是否存在**未被引用**的标识符 name：先把引号片段整体
// 抹掉，再在剩下的文本里找词元——「把引号替换成空格」的写法会把 `key` 里的
// key 当成本裸词，自证无效。
func bareIdent(stmt, name string) bool {
	clean := quotedSeg.ReplaceAllString(stmt, " ")
	for _, m := range identRe.FindAllString(clean, -1) {
		if m == name {
			return true
		}
	}
	return false
}

// TestMySQLStatementsQuoteReservedKey MySQL 方言下每条语句都必须引用 `key`
// ——它是保留字，裸用即语法错误（queue#32）。
//
// 失效形态：建表 `key VARCHAR(64) PRIMARY KEY` 直接报语法错；UPDATE/DELETE 的
// `WHERE key = ?` 同样报错 ⇒ MySQL 分支整条不可用，而三方言矩阵只覆盖 SQLite
// （SQLite 不把 key 当保留字），缺陷一路静默。
func TestMySQLStatementsQuoteReservedKey(t *testing.T) {
	d := &recDriver{}
	sql.Register("sqllease_rec_mysql", d) // 同一进程只注册一次：库名唯一
	db, err := sql.Open("sqllease_rec_mysql", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := New(db, MySQL)

	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TryAcquire(ctx, "lead", "a", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.Release(ctx, "lead", "a"); err != nil {
		t.Fatal(err)
	}

	stmts := d.taken()
	if len(stmts) < 3 {
		t.Fatalf("应记录建表 + 取 + 放三条语句，实得 %d: %v", len(stmts), stmts)
	}
	for _, stmt := range stmts {
		for _, ident := range []string{"key", "holder", "expires_at"} {
			if bareIdent(stmt, ident) {
				t.Fatalf("%s 未被引用（MySQL 保留字/一致性）：\n%s", ident, stmt)
			}
		}
		if !strings.Contains(stmt, "`key`") {
			t.Fatalf("MySQL 方言应使用反引号引用 key：\n%s", stmt)
		}
	}
}

// TestMySQLMigrateDDLIsBackticked 建表语句单独钉一次（最容易被漏改的一条）。
func TestMySQLMigrateDDLIsBackticked(t *testing.T) {
	d := &recDriver{}
	sql.Register("sqllease_rec_mysql_ddl", d)
	db, err := sql.Open("sqllease_rec_mysql_ddl", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := New(db, MySQL).Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	stmts := d.taken()
	if len(stmts) != 1 {
		t.Fatalf("应只有一条建表语句: %v", stmts)
	}
	if !strings.Contains(stmts[0], "`key` VARCHAR(64) PRIMARY KEY") {
		t.Fatalf("MySQL 建表语句必须反引号引用 key（保留字）：\n%s", stmts[0])
	}
}

// TestPostgresUsesStdQuotes 成对对照：Postgres 走标准双引号，而不是反引号
// （反引号在 PG 里是语法错误）。SQLite 分支由既有真库用例覆盖（两种引号都接受）。
func TestPostgresUsesStdQuotes(t *testing.T) {
	d := &recDriver{}
	sql.Register("sqllease_rec_pg", d)
	db, err := sql.Open("sqllease_rec_pg", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := New(db, Postgres).Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	stmts := d.taken()
	if len(stmts) != 1 {
		t.Fatalf("应只有一条建表语句: %v", stmts)
	}
	if !strings.Contains(stmts[0], `"key" VARCHAR(64) PRIMARY KEY`) {
		t.Fatalf("Postgres 建表语句应用双引号：\n%s", stmts[0])
	}
	if strings.Contains(stmts[0], "`") {
		t.Fatalf("Postgres 语句不得出现反引号：\n%s", stmts[0])
	}
}
