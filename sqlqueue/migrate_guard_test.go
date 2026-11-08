package sqlqueue

import (
	"context"
	"strings"
	"testing"
)

// MySQL 索引判存的 schema 限定（queue#87）。
//
// 失效形态：information_schema.statistics 是**跨库**视图，只按 table_name +
// index_name 判存时，同一 MySQL 实例上任意一个 schema 里有同名表 + 同名索引
// （索引名由表名派生：dev/staging/prod 三库并存、mysqldump 复制库、多租户
// 每租户一 schema 都命中），本 schema 的 Migrate 就会误判「已存在」并跳过，
// 索引**永不创建**且零报错——任务表是高频读写面，缺索引直接表现为吞吐下降
// 与 DB CPU 上涨，排查方向会被引向「任务太多」而不是「索引压根没建」。
func TestMySQLIndexExistenceCheckScopesSchema(t *testing.T) {
	s, d := newMigStubStore(t, MySQL, 0)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	stmts := d.taken()
	found := false
	for _, st := range stmts {
		if !strings.Contains(st.query, "information_schema.statistics") {
			continue
		}
		found = true
		if !strings.Contains(st.query, "table_schema") {
			t.Fatalf("MySQL 索引判存必须带 table_schema 谓词（跨库视图按表名+索引名判存会命中他 schema 的同名索引）: %q", st.query)
		}
		if !strings.Contains(st.query, "DATABASE()") {
			t.Fatalf("判存应绑定当前库（DATABASE()）：跨 schema 复用同一 driver/连接池时只有它能锚定本库: %q", st.query)
		}
		// 绑定参数：table_name 与 index_name（schema 由 DATABASE() 在库内取，
		// 不走参数——避开个别代理/中间件对语句内函数的支持差异不改变参数面）。
		if len(st.args) != 2 {
			t.Fatalf("判存应绑定 table_name 与 index_name 两个参数，实得 %v", st.args)
		}
		if st.args[0] != s.table {
			t.Fatalf("判存第一个绑定参数应是表名 %q: %v", s.table, st.args)
		}
		wantIdx := []string{s.table + "_status_created", s.table + "_runner_status"}
		ok := false
		for _, w := range wantIdx {
			if st.args[1] == w {
				ok = true
			}
		}
		if !ok {
			t.Fatalf("判存第二个绑定参数应是派生的索引名 %v: %v", wantIdx, st.args)
		}
	}
	if !found {
		t.Fatalf("MySQL 方言应走 information_schema 判存，实发语句: %v", stmts)
	}
}

// TestMySQLIndexExistenceShortCircuits 成对对照：判存命中必须**只**跳过建索引
// （不是跳过整个 Migrate，也不是无条件重发 ALTER）；未命中必须真的发出 ALTER。
// 只断言「建出来了」会漏掉「第二次跑炸了 / 或每次都重复建」两个方向。
func TestMySQLIndexExistenceShortCircuits(t *testing.T) {
	// 判存返回 2（两个索引都已存在）→ 不得发任何 ALTER。
	s, d := newMigStubStore(t, MySQL, 2)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("幂等重跑应无错误: %v", err)
	}
	for _, st := range d.taken() {
		if strings.HasPrefix(strings.TrimSpace(st.query), "ALTER TABLE") {
			t.Fatalf("判存命中后不得再发 ALTER（索引已存在）: %q", st.query)
		}
	}

	// 判存返回 0 → 两个索引各发一条 ALTER（判存不是一刀切跳过）。
	s2, d2 := newMigStubStore(t, MySQL, 0)
	if err := s2.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	alters := 0
	for _, st := range d2.taken() {
		if strings.HasPrefix(strings.TrimSpace(st.query), "ALTER TABLE") {
			alters++
		}
	}
	if alters != 2 {
		t.Fatalf("判存未命中应建两个索引，实发 %d 条 ALTER: %v", alters, d2.taken())
	}
}

// TestMigrateIdempotentSQLite 真 SQLite 的幂等对照（同 schema 重复跑 Migrate
// 不报错、不重复建索引；SQLite 分支不走 information_schema，天然幂等路径由
// CREATE INDEX IF NOT EXISTS 保证——与 MySQL 判存路径成对）。
func TestMigrateIdempotentSQLite(t *testing.T) {
	ctx := context.Background()
	s := testStore(t, "runner-a") // testStore 内已跑过一次 Migrate
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("重复 Migrate 应幂等无错误: %v", err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name LIKE 'agentkit_task_%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("应恰有两个业务索引，实得 %d", n)
	}
}
