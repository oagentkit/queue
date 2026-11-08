// 外部测试包（pglease_test）：同 fencing_token_test.go 的理由——worker
// re-export pglease，包内测试再引 worker 会成环。
package pglease_test

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 本文件钉 queue#89/#90 的语句形态回归：
//
//   - #90：TryAcquire（LeaderElector.tick 唯一获取路径）的 UPSERT 必须带
//     「易主才 +1」的 generation CASE——否则「选举用 elector + 关键写取
//     token」的文档化组合里，elector 夺走租约那一刻旧主的在飞 fenced 写
//     恰好照写不误（fencing 在它存在的全部理由那一刻失效）；
//   - #89：fencing token 必须**跨租约行生命周期**存续——Release 对 fenced
//     行置墓碑（generation 保留）、Cleanup 免删 fenced 行。直接删行的话
//     新行从 1 起步，stale 旧 token 反而成为最高有效 token（方向性反转）。

// TestTryAcquireBumpsGenerationOnTakeover elector 路径的易主必须推高
// generation（与 token 路径同款 CASE 分支结构）。
func TestTryAcquireBumpsGenerationOnTakeover(t *testing.T) {
	s, d := newFencingStore(t, 3, true)
	if _, err := s.TryAcquire(context.Background(), "k", "replica-a", time.Second); err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	stmts := d.recorded()
	if len(stmts) != 1 {
		t.Fatalf("TryAcquire 应只发一条语句，实得 %d 条: %v", len(stmts), stmts)
	}
	q := stmts[0].query
	if !strings.Contains(q, "ON CONFLICT (key) DO UPDATE") {
		t.Fatalf("必须是 UPSERT: %q", q)
	}
	cond := segmentBetween(t, q, "CASE WHEN", "THEN")
	if !strings.Contains(cond, "holder") || !strings.Contains(cond, "EXCLUDED.holder") {
		t.Fatalf("分支条件必须是「holder 换没换」: %q", cond)
	}
	thenBranch := segmentBetween(t, q, "THEN", "ELSE")
	if !strings.Contains(thenBranch, "generation") || strings.Contains(thenBranch, "+ 1") {
		t.Fatalf("续约支必须取原 generation（不得 +1）: %q", thenBranch)
	}
	elseBranch := segmentBetween(t, q, "ELSE", "END")
	if !strings.Contains(elseBranch, "+ 1") {
		t.Fatalf("易主支必须 +1——elector 夺约不推高 generation，fencing 对 elector 路径失效（#90）: %q", elseBranch)
	}
	// 首次插入显式写 generation 1（消掉裸列 DEFAULT 的隐性复位面）。
	if !strings.Contains(q, "VALUES ($1, $2, now() + make_interval(secs => $3), 1)") {
		t.Fatalf("首次插入的 generation 应为显式字面 1: %q", q)
	}
}

// TestReleaseSparesFencedRows fenced 行的 Release 是墓碑不是删除：
// 一条语句内两条分支（删非 fenced / 墓碑 fenced），墓碑保留 generation、
// 清空 holder、expires_at 置过去（下一刻即可被夺——让位语义不因保 token 变慢）。
func TestReleaseSparesFencedRows(t *testing.T) {
	s, d := newFencingStore(t, 3, true)
	if err := s.Release(context.Background(), "k", "replica-a"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	stmts := d.recorded()
	if len(stmts) != 1 {
		t.Fatalf("Release 应只发一条语句（同一快照内二选一，不留「先读后删」窗口），实得 %d 条: %v", len(stmts), stmts)
	}
	q := stmts[0].query
	delBranch := segmentBetween(t, q, "DELETE FROM", "RETURNING")
	if !strings.Contains(delBranch, "NOT fenced") {
		t.Fatalf("删除分支必须豁免 fenced 行——删掉 fenced 行 = token 复位（#89）: %q", delBranch)
	}
	if !strings.Contains(delBranch, "holder = $2") {
		t.Fatalf("仍须仅持有者本人可释放: %q", delBranch)
	}
	tombBranch := segmentBetween(t, q, "UPDATE", "RETURNING")
	if !strings.Contains(tombBranch, "fenced") {
		t.Fatalf("必须有 fenced 行分支: %q", tombBranch)
	}
	if strings.Contains(tombBranch, "generation") {
		t.Fatalf("墓碑分支不得触碰 generation——它要跨行生命周期存续: %q", tombBranch)
	}
	if !strings.Contains(tombBranch, "holder = ''") {
		t.Fatalf("墓碑应清空 holder（释放者本人的再获取也走易主分支 +1）: %q", tombBranch)
	}
	if !strings.Contains(tombBranch, "expires_at") || !strings.Contains(tombBranch, "make_interval") {
		t.Fatalf("墓碑应把 expires_at 置到过去（下一刻即可被夺，让位语义不因保 token 变慢）: %q", tombBranch)
	}
}

// TestCleanupSparesFencedRows Cleanup 免删 fenced 行（generation 是业务行
// CAS 的高水位，行在 token 在）；非 fenced 行的过期清理语义不变。
func TestCleanupSparesFencedRows(t *testing.T) {
	s, d := newRecordingStore(t, 2)
	n, err := s.Cleanup(context.Background())
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if n != 2 {
		t.Fatalf("删除行数 = %d, want 2", n)
	}
	stmts := d.queries()
	if len(stmts) != 1 {
		t.Fatalf("Cleanup 应只发一条语句，实得 %d 条: %v", len(stmts), stmts)
	}
	q := stmts[0].query
	if !strings.HasPrefix(strings.TrimSpace(q), "DELETE FROM") {
		t.Fatalf("语句不是 DELETE: %q", q)
	}
	if !strings.Contains(q, "expires_at < now()") {
		t.Fatalf("过期判据必须是 `expires_at < now()`（DB 时钟）: %q", q)
	}
	if !strings.Contains(q, "NOT fenced") {
		t.Fatalf("Cleanup 必须豁免 fenced 行——清掉后新行从 1 起步，stale 旧 token 反成最高有效 token（#89）: %q", q)
	}
}

// TestTokenPathMarksRowsFenced token 路径的 UPSERT 必须给行打 fenced 标记
// （INSERT 带初值、冲突更新置 TRUE）——它是 Release/Cleanup 免删的判据。
func TestTokenPathMarksRowsFenced(t *testing.T) {
	s, d := newFencingStore(t, 3, true)
	if _, _, err := s.TryAcquireWithToken(context.Background(), "k", "replica-a", time.Second); err != nil {
		t.Fatalf("TryAcquireWithToken: %v", err)
	}
	q := d.recorded()[0].query
	if !strings.Contains(q, "fenced") {
		t.Fatalf("token 路径必须打 fenced 标记（Release/Cleanup 免删的判据）: %q", q)
	}
	insertSeg := q[:strings.Index(q, "ON CONFLICT")]
	if !strings.Contains(insertSeg, ", TRUE)") {
		t.Fatalf("首次插入就应置 fenced=TRUE: %q", q)
	}
	conflictSeg := q[strings.Index(q, "DO UPDATE SET"):]
	if !strings.Contains(conflictSeg, "fenced     = TRUE") && !strings.Contains(conflictSeg, "fenced = TRUE") {
		t.Fatalf("冲突更新应置 fenced=TRUE（elector 建的行首次走 token 路径时打标）: %q", q)
	}
}
