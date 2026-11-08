package sqlqueue

import (
	"context"
	"testing"
	"time"
)

// 毒任务重试上限与死信面（oagentkit/queue#70）。
//
// 修复前的失败几何：claim（attempts+1）→ Run panic → 心跳冻结（#42 修复后
// 正确地不续它）→ staleAfter 后 ResetRunningToPending 复位（attempts 再 +1）
// → FIFO 又把它排到队首 → 永远先被领走。一条死任务每 90s 烧一轮 claim+run，
// 无上限、无终态、attempts 对 TaskQueue 接口不可见。

// poisonCycle 跑一轮毒任务生命周期：领取 → 心跳冻结（回拨模拟 staleAfter 流逝）
// → 失联复位。返回本轮是否领到了任务。
func poisonCycle(t *testing.T, ctx context.Context, s *Store, taskID string) bool {
	t.Helper()
	id, ok, err := s.ClaimNextPending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		return false
	}
	if id != taskID {
		t.Fatalf("FIFO 应领到 %s: %q", taskID, id)
	}
	backdate(t, s, 3600, taskID)
	if _, err := s.ResetRunningToPending(ctx, 90*time.Second); err != nil {
		t.Fatal(err)
	}
	return true
}

func statusOf(t *testing.T, ctx context.Context, s *Store, taskID string) string {
	t.Helper()
	var status string
	if err := s.db.QueryRowContext(ctx,
		`SELECT status FROM `+s.table+` WHERE task_id = `+s.ph(1), taskID).Scan(&status); err != nil {
		t.Fatalf("读 %s 状态: %v", taskID, err)
	}
	return status
}

func attemptsOf(t *testing.T, ctx context.Context, s *Store, taskID string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT attempts FROM `+s.table+` WHERE task_id = `+s.ph(1), taskID).Scan(&n); err != nil {
		t.Fatalf("读 %s attempts: %v", taskID, err)
	}
	return n
}

// WithMaxAttempts：毒任务在有限轮内进入终态 dead_letter，从此退出重试循环
// （claim 只取 pending，死信行永不再被领取）。
//
// 次数语义（queue#86）：WithMaxAttempts(4) = 恰好执行 4 次。attempts 计数口径
// 是 claim 与 reset 各 +1，复位时点已执行 k 次 ⟺ attempts = 2k-1；终态判据
// attempts ≥ 2n-1。
func TestMaxAttemptsDeadLettersPoisonTask(t *testing.T) {
	ctx := context.Background()
	s := testStore(t, "runner-a").WithMaxAttempts(4)
	if err := s.Enqueue(ctx, "poison"); err != nil {
		t.Fatal(err)
	}

	// 第 1 轮：claim(attempts=1，已执行 1 次) → 复位时 1 < 4 次 → 回 pending（attempts=2）
	if !poisonCycle(t, ctx, s, "poison") {
		t.Fatal("第 1 轮应领到 poison")
	}
	if st := statusOf(t, ctx, s, "poison"); st != StatusPending {
		t.Fatalf("已执行 1 次（<4），应回 pending: %s", st)
	}
	// 第 2 轮：claim(attempts=3，已执行 2 次) → 回 pending（attempts=4）
	if !poisonCycle(t, ctx, s, "poison") {
		t.Fatal("第 2 轮应领到 poison")
	}
	if st := statusOf(t, ctx, s, "poison"); st != StatusPending {
		t.Fatalf("已执行 2 次（<4），应回 pending: %s", st)
	}
	// 第 3 轮：claim(attempts=5，已执行 3 次) → 回 pending（attempts=6）
	if !poisonCycle(t, ctx, s, "poison") {
		t.Fatal("第 3 轮应领到 poison")
	}
	if st := statusOf(t, ctx, s, "poison"); st != StatusPending {
		t.Fatalf("已执行 3 次（<4），应回 pending: %s", st)
	}
	// 第 4 轮：claim(attempts=7，已执行 4 次) → 复位时 4 ≥ 4 → 终态 dead_letter
	if !poisonCycle(t, ctx, s, "poison") {
		t.Fatal("第 4 轮应领到 poison")
	}
	if st := statusOf(t, ctx, s, "poison"); st != StatusDeadLetter {
		t.Fatalf("已执行满 4 次，应置终态 dead_letter: %s", st)
	}
	frozen := attemptsOf(t, ctx, s, "poison")

	// 终态后退出重试循环：不再可领，复位/续期都不再碰它
	for i := 0; i < 3; i++ {
		if poisonCycle(t, ctx, s, "poison") {
			t.Fatalf("dead_letter 行不得再被领取（第 %d 轮）", i+4)
		}
	}
	if got := attemptsOf(t, ctx, s, "poison"); got != frozen {
		t.Fatalf("dead_letter 后 attempts 应定格在 %d: %d", frozen, got)
	}
	if st := statusOf(t, ctx, s, "poison"); st != StatusDeadLetter {
		t.Fatalf("多轮 claim/reset 后仍应停在 dead_letter: %s", st)
	}
	// 队列对其他任务照常可用（死信不堵队列）
	if err := s.Enqueue(ctx, "fresh"); err != nil {
		t.Fatal(err)
	}
	if id, ok, err := s.ClaimNextPending(ctx); err != nil || !ok || id != "fresh" {
		t.Fatalf("死信不得堵队列: id=%q ok=%v err=%v", id, ok, err)
	}
}

// 零值 = 既有行为：不设上限时毒任务照旧无限重试（兼容性红线——默认路径的
// SQL 与状态迁移一字不变）。
func TestZeroMaxAttemptsKeepsExistingBehavior(t *testing.T) {
	ctx := context.Background()
	s := testStore(t, "runner-a")
	if err := s.Enqueue(ctx, "poison"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if !poisonCycle(t, ctx, s, "poison") {
			t.Fatalf("无上限时第 %d 轮应照旧重试（复位回 pending）", i+1)
		}
		if st := statusOf(t, ctx, s, "poison"); st != StatusPending {
			t.Fatalf("无上限时不得进终态: %s", st)
		}
	}
	if n, err := s.ResetRunningToPending(ctx, time.Hour); err != nil || n != 0 {
		t.Fatalf("心跳新鲜（回拨只在轮内）不应误复位: n=%d err=%v", n, err)
	}
}

// 负数上限与零值同义（不封顶），WithMaxAttempts 支持链式且不误伤其他配置。
func TestNegativeMaxAttemptsMeansNoCap(t *testing.T) {
	ctx := context.Background()
	s := testStore(t, "runner-a").WithMaxAttempts(-1)
	if err := s.Enqueue(ctx, "poison"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if !poisonCycle(t, ctx, s, "poison") {
			t.Fatalf("负上限=不封顶，第 %d 轮应照旧重试", i+1)
		}
	}
}

// ClaimNextPendingDetailed：attempts 对领取侧可见（可选接口面），宿主无需直查
// 内部表就能实现自己的毒任务闸门。attempts 是含本次领取的计数。
func TestClaimNextPendingDetailedSurfacesAttempts(t *testing.T) {
	ctx := context.Background()
	s := testStore(t, "runner-a")
	if err := s.Enqueue(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	id, attempts, ok, err := s.ClaimNextPendingDetailed(ctx)
	if err != nil || !ok || id != "t1" {
		t.Fatalf("detailed claim#1 = %q attempts=%d ok=%v err=%v", id, attempts, ok, err)
	}
	if attempts != 1 {
		t.Fatalf("首次领取 attempts = %d, want 1", attempts)
	}
	// 失联复位（attempts 再 +1 → 2）后重领：attempts=3（含本次）
	backdate(t, s, 3600, "t1")
	if _, err := s.ResetRunningToPending(ctx, 90*time.Second); err != nil {
		t.Fatal(err)
	}
	id, attempts, ok, err = s.ClaimNextPendingDetailed(ctx)
	if err != nil || !ok || id != "t1" {
		t.Fatalf("detailed claim#2 = %q attempts=%d ok=%v err=%v", id, attempts, ok, err)
	}
	if attempts != 3 {
		t.Fatalf("复位后重领 attempts = %d, want 3（claim+reset+claim）", attempts)
	}
	// 队列空：(…, 0, false, nil)，attempts 无意义
	if _, attempts, ok, err := s.ClaimNextPendingDetailed(ctx); err != nil || ok || attempts != 0 {
		t.Fatalf("空队列 detailed claim = attempts=%d ok=%v err=%v", attempts, ok, err)
	}
	// TaskQueue 三方法主路径不受影响（ClaimNextPending 仍走同一实现）
	if err := s.Enqueue(ctx, "t2"); err != nil {
		t.Fatal(err)
	}
	if id, ok, err := s.ClaimNextPending(ctx); err != nil || !ok || id != "t2" {
		t.Fatalf("普通 claim = %q ok=%v err=%v", id, ok, err)
	}
}
