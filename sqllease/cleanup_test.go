package sqllease

import (
	"context"
	"strings"
	"testing"
)

// 过期租约清理（queue#84，pglease#33 的三方言移植）。
//
// 判据（pglease/cleanup_test.go 同款，三方言各跑一遍）：
//   - 语句必须含方言 now 表达式而**不是占位符参数**——参数化 cutoff 是应用
//     时钟，正是要禁止的形态（应用时钟偏慢的副本会删掉还没过期的行，等于替
//     对端提前让位，双主窗口凭空打开）；
//   - 必须是 `<` 而非 `<=`（`<=` 会删掉边界上活着的租约）；
//   - RowsAffected 必须透传（周期清理没有观测面就无法判断是否在跑）。
func TestCleanupDeletesExpiredRowsByDBClock(t *testing.T) {
	for _, dialect := range allDialects() {
		t.Run(dialectName(dialect), func(t *testing.T) {
			s, d := newStubStore(t, dialect, 7)
			n, err := s.Cleanup(context.Background())
			if err != nil {
				t.Fatalf("Cleanup: %v", err)
			}
			if n != 7 {
				t.Fatalf("删除行数 = %d, want 7——RowsAffected 未透传，周期清理就没有观测面", n)
			}
			stmts := d.taken()
			if len(stmts) != 1 {
				t.Fatalf("Cleanup 应只发一条语句，实得 %d 条: %v", len(stmts), stmts)
			}
			q := stmts[0].query
			if !strings.HasPrefix(strings.TrimSpace(q), "DELETE FROM") {
				t.Fatalf("语句不是 DELETE: %q", q)
			}
			if !strings.Contains(q, DefaultTable) {
				t.Fatalf("语句未指向配置的表: %q", q)
			}
			if !strings.Contains(q, "expires_at") || !strings.Contains(q, " < ") {
				t.Fatalf("过期判据必须是 `expires_at < <DB now>`（DB 时钟）——实得 %q。"+
					"漏 WHERE 会删掉活租约，用应用时钟则与 TryAcquire 的过期判定不同钟", q)
			}
			if strings.Contains(q, "<=") {
				t.Fatalf("过期判据必须是严格小于 `<`（`<=` 会删掉边界上活着的租约）: %q", q)
			}
			// 方言 now 表达式（与 ExpiredPredicate 同源）：
			var nowExpr, phMark string
			switch dialect {
			case Postgres:
				nowExpr, phMark = "now()", "$"
			case MySQL:
				nowExpr, phMark = "NOW()", "?"
			default:
				nowExpr, phMark = "strftime('%s','now')", "?"
			}
			if !strings.Contains(q, nowExpr) {
				t.Fatalf("清理判据必须用本方言 DB 时钟表达式 %q: %q", nowExpr, q)
			}
			if strings.Contains(q, phMark) {
				t.Fatalf("判据不得出现占位符 %q——参数化 cutoff 是应用时钟，正是要禁止的形态: %q", phMark, q)
			}
			if len(stmts[0].args) != 0 {
				t.Fatalf("DB 时钟判据不该带参数，实得 args=%v", stmts[0].args)
			}
			// 判据与 ExpiredPredicate 单源：Cleanup 的 WHERE 必须逐字含它，
			// 两处各写一份即时钟漂移。
			if !strings.Contains(q, s.ExpiredPredicate()) {
				t.Fatalf("Cleanup 判据应由 ExpiredPredicate 单源派生:\n语句: %q\n判据: %q", q, s.ExpiredPredicate())
			}
		})
	}
}

// TestExpiredPredicateDialectShapes ExpiredPredicate 是方言时钟判据的公共出口
// （queue#84）：三方言各自的确切形态 + 严格小于。调用方定向清理（按前缀/
// 租户）拼 WHERE 全靠它——形态漂移即双钟。
func TestExpiredPredicateDialectShapes(t *testing.T) {
	cases := map[Dialect]string{
		Postgres: `"expires_at" < now()`,
		MySQL:    "`expires_at` < NOW()",
		SQLite:   `"expires_at" < strftime('%s','now')`,
	}
	for dialect, want := range cases {
		t.Run(dialectName(dialect), func(t *testing.T) {
			s, _ := newStubStore(t, dialect, 0)
			if got := s.ExpiredPredicate(); got != want {
				t.Fatalf("ExpiredPredicate = %q, want %q", got, want)
			}
		})
	}
}

// TestCleanupKeepsUnexpiredRows 成对对照：判据不得退化成「清空表」。
//
// 只测「有 DELETE 语句」是测不出判据方向的——`DELETE FROM t`（无 WHERE）同样
// 通过。这里让 stub 报 0 行，断言调用方拿到 0（真实库里「无过期行」正是 0），
// 并把「不得出现无条件 DELETE」钉在语句形态上。
func TestCleanupKeepsUnexpiredRows(t *testing.T) {
	for _, dialect := range allDialects() {
		t.Run(dialectName(dialect), func(t *testing.T) {
			s, d := newStubStore(t, dialect, 0)
			n, err := s.Cleanup(context.Background())
			if err != nil {
				t.Fatalf("Cleanup: %v", err)
			}
			if n != 0 {
				t.Fatalf("无过期行时应返回 0，实得 %d", n)
			}
			q := d.taken()[0].query
			if !strings.Contains(q, "WHERE") {
				t.Fatalf("清理语句必须带 WHERE，否则会连活着的租约一起删掉（独占性直接失守）: %q", q)
			}
		})
	}
}
