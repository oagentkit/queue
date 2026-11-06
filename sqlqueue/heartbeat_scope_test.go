package sqlqueue

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// hbOf 读某任务当前 heartbeat_at（未写过为 NULL）。
func hbOf(t *testing.T, s *Store, taskID string) sql.NullFloat64 {
	t.Helper()
	var hb sql.NullFloat64
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT heartbeat_at FROM `+s.table+` WHERE task_id = `+s.ph(1), taskID).Scan(&hb); err != nil {
		t.Fatalf("读 %s 心跳: %v", taskID, err)
	}
	return hb
}

// backdate 把任务心跳回拨 secs 秒（模拟时间流逝，避免真等 staleAfter）。
func backdate(t *testing.T, s *Store, secs int, taskIDs ...string) {
	t.Helper()
	for _, id := range taskIDs {
		if _, err := s.db.ExecContext(context.Background(),
			`UPDATE `+s.table+` SET heartbeat_at = strftime('%s','now') - `+s.ph(1)+
				` WHERE task_id = `+s.ph(2), secs, id); err != nil {
			t.Fatalf("回拨 %s 心跳: %v", id, err)
		}
	}
}

func hbFresh(t *testing.T, s *Store, taskID string) bool {
	t.Helper()
	hb := hbOf(t, s, taskID)
	if !hb.Valid {
		t.Fatalf("%s 心跳应非 NULL", taskID)
	}
	return hb.Float64 > float64(time.Now().Unix()-30)
}

// TestTouchHeartbeatsForScoped 续期集之外的行心跳必须冻结（oagentkit/queue#42）。
//
// 三重守卫逐条钉住：task_id IN (...) 是本次修复的实质，runner_id 与
// status='running' 是既有归属语义不能丢。
func TestTouchHeartbeatsForScoped(t *testing.T) {
	ctx := context.Background()
	a := testStore(t, "runner-a")
	b := New(a.db, SQLite, "runner-b") // 同一物理表，两个实例身份

	for _, id := range []string{"ta1", "ta2", "tb1"} {
		if err := a.Enqueue(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	// created_at 同秒 ⇒ 排序落到 task_id，claim 顺序可复现
	for _, want := range []string{"ta1", "ta2"} {
		if id, ok, err := a.ClaimNextPending(ctx); err != nil || !ok || id != want {
			t.Fatalf("a claim = %q ok=%v err=%v, want %s", id, ok, err, want)
		}
	}
	if id, ok, err := b.ClaimNextPending(ctx); err != nil || !ok || id != "tb1" {
		t.Fatalf("b claim = %q ok=%v err=%v, want tb1", id, ok, err)
	}

	// ---- ① task_id 守卫：只续期 ta1，ta2 必须冻结 ----
	backdate(t, a, 3600, "ta1", "ta2", "tb1")
	if err := a.TouchHeartbeatsFor(ctx, []string{"ta1"}); err != nil {
		t.Fatal(err)
	}
	if !hbFresh(t, a, "ta1") {
		t.Error("ta1 在续期集内，心跳应被刷新")
	}
	if hbFresh(t, a, "ta2") {
		t.Error("ta2 不在续期集内，心跳必须冻结——否则 panic 退出的任务永不复位（#42 根因）")
	}

	// ---- ② runner_id 守卫：a 点名 tb1 也不得续到它 ----
	backdate(t, a, 3600, "tb1")
	if err := a.TouchHeartbeatsFor(ctx, []string{"tb1"}); err != nil {
		t.Fatal(err)
	}
	if hbFresh(t, a, "tb1") {
		t.Error("tb1 归 runner-b，runner-a 不得续期他实例的任务")
	}

	// ---- ③ status 守卫：已终态的行不得被续回新鲜 ----
	if _, err := a.db.ExecContext(ctx, `UPDATE `+a.table+` SET status = 'done' WHERE task_id = 'ta1'`); err != nil {
		t.Fatal(err)
	}
	backdate(t, a, 3600, "ta1")
	if err := a.TouchHeartbeatsFor(ctx, []string{"ta1"}); err != nil {
		t.Fatal(err)
	}
	if hbFresh(t, a, "ta1") {
		t.Error("ta1 已是 done，不得被续期")
	}

	// ---- ④ 空集：不报错、不动任何行（绝不能退化成无过滤条件的盲续）----
	backdate(t, a, 3600, "ta2", "tb1")
	if err := a.TouchHeartbeatsFor(ctx, nil); err != nil {
		t.Fatalf("空集应直接返回: %v", err)
	}
	if err := a.TouchHeartbeatsFor(ctx, []string{}); err != nil {
		t.Fatalf("空切片应直接返回: %v", err)
	}
	if hbFresh(t, a, "ta2") || hbFresh(t, a, "tb1") {
		t.Error("空集不得续期任何行")
	}
}

// TestScopedHeartbeatUnblocksStaleRecovery 端到端：证明「任务退出后心跳冻结
// ⇒ ResetRunningToPending 能命中」，即 pool.runTask 注释宣称的恢复契约。
//
// 对照组是同批行里的「仍在跑」任务——它必须**不**被复位，否则修法就变成了
// 「把所有 running 都当失联」。
func TestScopedHeartbeatUnblocksStaleRecovery(t *testing.T) {
	ctx := context.Background()
	a := testStore(t, "runner-a")

	for _, id := range []string{"alive", "gone"} {
		if err := a.Enqueue(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, _ := a.ClaimNextPending(ctx); !ok {
		t.Fatal("应抢到 alive")
	}
	if _, ok, _ := a.ClaimNextPending(ctx); !ok {
		t.Fatal("应抢到 gone")
	}
	// 两个任务都是本实例的 running 行——盲续分不清它们，续期集能
	backdate(t, a, 3600, "alive", "gone")

	if err := a.TouchHeartbeatsFor(ctx, []string{"alive"}); err != nil {
		t.Fatal(err)
	}
	n, err := a.ResetRunningToPending(ctx, 90*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reset = %d, want 1（gone 失联复位、alive 在跑不复位）", n)
	}
	var status, runner sql.NullString
	if err := a.db.QueryRowContext(ctx,
		`SELECT status, runner_id FROM `+a.table+` WHERE task_id = 'gone'`).Scan(&status, &runner); err != nil {
		t.Fatal(err)
	}
	if status.String != "pending" || runner.Valid {
		t.Fatalf("gone 应回 pending 且归属清空: status=%q runner=%v", status.String, runner)
	}
	if err := a.db.QueryRowContext(ctx,
		`SELECT status FROM `+a.table+` WHERE task_id = 'alive'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status.String != "running" {
		t.Fatalf("alive 应仍在 running: %s", status.String)
	}

	// 复位后的 gone 可被重新领取（恢复路径真闭环，不只是状态字符串变了）
	if id, ok, err := a.ClaimNextPending(ctx); err != nil || !ok || id != "gone" {
		t.Fatalf("复位后应能重领 gone: id=%q ok=%v err=%v", id, ok, err)
	}
}
