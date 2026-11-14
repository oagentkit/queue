package outbox

import (
	"strings"
	"testing"
)

// dialectQueue 只为取 SQL 文本：构建器是纯文本函数，不触库（nil db 安全）。
func dialectQueue(d Dialect, table string) *Queue {
	q := &Queue{dialect: d, table: table, staleAfter: defaultStaleAfter, pollInterval: defaultPollInterval}
	if table == "" {
		q.table = DefaultTable
	}
	return q
}

// TestUpsertSQLDialectMatrix 幂等 upsert 的方言矩阵：PG 走 ON CONFLICT +
// excluded + $N 序号占位；MySQL 走 ON DUPLICATE KEY UPDATE + VALUES()；
// SQLite 与 PG 同构但用 ? 占位与 strftime 时钟。
func TestUpsertSQLDialectMatrix(t *testing.T) {
	pg := dialectQueue(Postgres, "")
	s := pg.upsertSQL()
	for _, frag := range []string{
		"INSERT INTO agentkit_outbox", "ON CONFLICT (kind, dedup_key) DO UPDATE",
		"excluded.payload", "excluded.not_before", "attempts = 0",
		"now() + make_interval(secs => $4)", "$1, $2, $3", "claim_token = NULL",
	} {
		if !strings.Contains(s, frag) {
			t.Errorf("PG upsert 缺 %q:\n%s", frag, s)
		}
	}
	if strings.Contains(s, "VALUES(payload)") {
		t.Errorf("PG upsert 混入 MySQL 语法:\n%s", s)
	}

	my := dialectQueue(MySQL, "")
	s = my.upsertSQL()
	for _, frag := range []string{
		"INSERT INTO agentkit_outbox", "ON DUPLICATE KEY UPDATE", "VALUES(payload)",
		"VALUES(not_before)", "attempts = 0", "DATE_ADD(NOW(), INTERVAL ? SECOND)",
		"claim_token = NULL", "?, ?, ?", "created_at = VALUES(created_at)",
	} {
		if !strings.Contains(s, frag) {
			t.Errorf("MySQL upsert 缺 %q:\n%s", frag, s)
		}
	}
	if strings.Contains(s, "ON CONFLICT") || strings.Contains(s, "$1") {
		t.Errorf("MySQL upsert 混入 PG 语法:\n%s", s)
	}

	lt := dialectQueue(SQLite, "")
	s = lt.upsertSQL()
	for _, frag := range []string{
		"INSERT INTO agentkit_outbox", "ON CONFLICT (kind, dedup_key) DO UPDATE",
		"excluded.payload", "strftime('%s','now') + ?", "strftime('%s','now')",
	} {
		if !strings.Contains(s, frag) {
			t.Errorf("SQLite upsert 缺 %q:\n%s", frag, s)
		}
	}
}

// TestClaimSQLDialectMatrix 认领三语句（候选提示 / 守卫 CAS / 命中重读）：
// 到期判定与 DB 时钟同表达式；attempts < 上限挡预算耗尽行；守卫把 pending
// → running 收进单语句。
func TestClaimSQLDialectMatrix(t *testing.T) {
	pg := dialectQueue(Postgres, "")
	candidate := pg.claimCandidateSQL()
	for _, frag := range []string{
		"WHERE kind = $1", "status = 'pending'", "attempts < $2",
		"not_before <= NOW()", "ORDER BY not_before, created_at, dedup_key", "LIMIT 1",
	} {
		if !strings.Contains(candidate, frag) {
			t.Errorf("PG 候选缺 %q:\n%s", frag, candidate)
		}
	}
	guard := pg.claimGuardSQL()
	for _, frag := range []string{
		"SET status = 'running', claim_token = $1", "WHERE kind = $2 AND dedup_key = $3",
		"status = 'pending' AND not_before <= NOW()",
	} {
		if !strings.Contains(guard, frag) {
			t.Errorf("PG 守卫缺 %q:\n%s", frag, guard)
		}
	}
	if reread := pg.claimRereadSQL(); !strings.Contains(reread, "SELECT payload, attempts") {
		t.Errorf("PG 重读缺载荷列:\n%s", reread)
	}

	my := dialectQueue(MySQL, "")
	if c := my.claimCandidateSQL(); !strings.Contains(c, "not_before <= NOW()") || !strings.Contains(c, "attempts < ?") {
		t.Errorf("MySQL 候选方言错:\n%s", c)
	}
	lt := dialectQueue(SQLite, "")
	if c := lt.claimCandidateSQL(); !strings.Contains(c, "not_before <= strftime('%s','now')") {
		t.Errorf("SQLite 候选未用 DB 时钟:\n%s", c)
	}
	if g := lt.claimGuardSQL(); strings.Contains(g, "$1") {
		t.Errorf("SQLite 守卫混入 PG 占位符:\n%s", g)
	}
}

// TestAccountingSQLTokenGuarded 四个记账面全部 token 守卫（清账/退避/死信/
// 延后）——守卫缺失 = 重入替换后旧认领改写新意图，at-least-once 契约破坏。
func TestAccountingSQLTokenGuarded(t *testing.T) {
	my := dialectQueue(MySQL, "")
	pg := dialectQueue(Postgres, "")

	checks := []struct {
		name string
		sql  string
		want []string
	}{
		{"清账", my.completeSQL(), []string{"DELETE FROM", "status = 'running' AND claim_token = ?"}},
		{"退避", my.failSQL(), []string{"attempts = attempts + 1", "not_before = DATE_ADD(NOW(), INTERVAL ? SECOND)", "claim_token = ?"}},
		{"死信", my.deadLetterSQL(), []string{"status = 'dead_letter'", "attempts = attempts + 1", "claim_token = ?"}},
		{"延后", my.deferSQL(), []string{"not_before = DATE_ADD(NOW(), INTERVAL ? SECOND)", "claim_token = ?", "status = 'pending'"}},
		{"清账PG", pg.completeSQL(), []string{"DELETE FROM", "status = 'running' AND claim_token = $3"}},
	}
	for _, c := range checks {
		for _, frag := range c.want {
			if !strings.Contains(c.sql, frag) {
				t.Errorf("%s 缺 %q:\n%s", c.name, frag, c.sql)
			}
		}
	}
	// 延后不得触碰 attempts（推后 ≠ 失败）
	if strings.Contains(my.deferSQL(), "attempts") {
		t.Errorf("延后语句不得改 attempts:\n%s", my.deferSQL())
	}
}

// TestResetStaleSQL stale 兜底：CASE 折算阈值（Go 侧算 M-1）、attempts+1、
// claimed_at 超窗判据用 DB 时钟。
func TestResetStaleSQL(t *testing.T) {
	pg := dialectQueue(Postgres, "")
	s := pg.resetStaleSQL()
	for _, frag := range []string{
		"CASE WHEN attempts >= $1 THEN 'dead_letter' ELSE 'pending' END",
		"attempts = attempts + 1", "claimed_at < now() - make_interval(secs => $3)",
		"WHERE kind = $2", "status = 'running'",
	} {
		if !strings.Contains(s, frag) {
			t.Errorf("PG stale 缺 %q:\n%s", frag, s)
		}
	}
	lt := dialectQueue(SQLite, "")
	if s := lt.resetStaleSQL(); !strings.Contains(s, "claimed_at < strftime('%s','now') - ?") {
		t.Errorf("SQLite stale 未用 DB 时钟:\n%s", s)
	}
}

// TestTableDDLDialectMatrix 建表 DDL：列宽由 MaxKindLen/MaxKeyLen 派生
// （护栏与 DDL 同源）；时间列类型 per 方言；payload MySQL 用 MEDIUMTEXT。
func TestTableDDLDialectMatrix(t *testing.T) {
	lt := dialectQueue(SQLite, "")
	s := lt.tableDDL()
	for _, frag := range []string{
		"CREATE TABLE IF NOT EXISTS agentkit_outbox",
		"kind        VARCHAR(64) NOT NULL", "dedup_key   VARCHAR(128) NOT NULL",
		"payload     TEXT NOT NULL", "REAL", "PRIMARY KEY (kind, dedup_key)",
		"DEFAULT 'pending'",
	} {
		if !strings.Contains(s, frag) {
			t.Errorf("SQLite DDL 缺 %q:\n%s", frag, s)
		}
	}
	my := dialectQueue(MySQL, "")
	if s := my.tableDDL(); !strings.Contains(s, "MEDIUMTEXT") || !strings.Contains(s, "DATETIME") {
		t.Errorf("MySQL DDL 缺 MEDIUMTEXT/DATETIME:\n%s", s)
	}
	if s := dialectQueue(Postgres, "").tableDDL(); !strings.Contains(s, "TIMESTAMPTZ") {
		t.Errorf("PG DDL 缺 TIMESTAMPTZ:\n%s", s)
	}
}

// TestIndexDDLMySQLAddIndex MySQL 无 CREATE INDEX IF NOT EXISTS：改走
// ADD INDEX + information_schema 判存（queue#87 先例）；PG/SQLite 走
// IF NOT EXISTS。
func TestIndexDDLMySQLAddIndex(t *testing.T) {
	my := dialectQueue(MySQL, "custom_outbox")
	ci, cs, ik, is := my.indexDDL()
	if !strings.Contains(ci, "ALTER TABLE custom_outbox ADD INDEX custom_outbox_kind_status") ||
		!strings.Contains(cs, "ALTER TABLE custom_outbox ADD INDEX custom_outbox_status_claimed") {
		t.Errorf("MySQL 索引应走 ADD INDEX:\n%s\n%s", ci, cs)
	}
	if ik != "custom_outbox_kind_status" || is != "custom_outbox_status_claimed" {
		t.Errorf("索引名派生错: %s/%s", ik, is)
	}
	ci2, _, _, _ := dialectQueue(SQLite, "custom_outbox").indexDDL()
	if !strings.Contains(ci2, "CREATE INDEX IF NOT EXISTS") {
		t.Errorf("SQLite 索引应走 IF NOT EXISTS:\n%s", ci2)
	}
}

// TestTableOverrideFlowsIntoSQL 表名覆盖流入全部构建器；非法表名静默忽略。
func TestTableOverrideFlowsIntoSQL(t *testing.T) {
	q := &Queue{dialect: SQLite, table: DefaultTable, staleAfter: defaultStaleAfter}
	q.WithTable("custom_outbox")
	for _, s := range []string{
		q.upsertSQL(), q.claimCandidateSQL(), q.claimGuardSQL(),
		q.claimRereadSQL(), q.completeSQL(), q.failSQL(),
		q.deadLetterSQL(), q.deferSQL(), q.resetStaleSQL(), q.countSQL(), q.tableDDL(),
	} {
		if !strings.Contains(s, "custom_outbox") || strings.Contains(s, DefaultTable) {
			t.Errorf("表名覆盖未生效:\n%s", s)
		}
	}
	q2 := &Queue{dialect: SQLite, table: DefaultTable}
	q2.WithTable("bad name; DROP")
	if q2.table != DefaultTable {
		t.Fatalf("非法表名未忽略: %s", q2.table)
	}
}
