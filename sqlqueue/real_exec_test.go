package sqlqueue

import (
	"context"
	"fmt"
	"testing"
)

// WithMaxAttempts 的真实执行次数语义（oagentkit/queue#86）。
//
// 修复前：claim 与失联复位各消耗一次计数、终态判定只看复位时的旧值
// （attempts ≥ n），实际执行次数是 floor(n/2)+1——WithMaxAttempts(3) 拿到
// 2 次。修复后：n = 任务至多被领取执行的次数（终态判据折算为 attempts ≥
// 2n-1），n=1..5 的实际执行次数必须逐条等于 1,2,3,4,5。

// TestMaxAttemptsMeansRealExecutions 表驱动：n=1..5，每个 n 用独立内存库，
// 领取 → 心跳冻结 → 复位循环到 claim 返回 false，断言实际执行次数恰为 n。
func TestMaxAttemptsMeansRealExecutions(t *testing.T) {
	for n := 1; n <= 5; n++ {
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			ctx := context.Background()
			s := testStore(t, "runner-a").WithMaxAttempts(n)
			if err := s.Enqueue(ctx, "task-1"); err != nil {
				t.Fatal(err)
			}
			runs := 0
			for i := 0; i < 20; i++ {
				if !poisonCycle(t, ctx, s, "task-1") {
					break
				}
				runs++
			}
			if runs != n {
				t.Fatalf("WithMaxAttempts(%d) 实际执行次数 = %d, want %d——API 名字承诺的是尝试次数", n, runs, n)
			}
			if st := statusOf(t, ctx, s, "task-1"); st != StatusDeadLetter {
				t.Fatalf("执行满 %d 次后应进终态 dead_letter: %s", n, st)
			}
			// attempts 计数口径不变（claim 与 reset 各 +1）：dead_letter 行定格 2n。
			if got := attemptsOf(t, ctx, s, "task-1"); got != 2*n {
				t.Fatalf("dead_letter 行 attempts 应定格在 2n=%d（计数口径不变）: %d", 2*n, got)
			}
		})
	}
}

// TestReplicaChurnDoesNotDeadLetterBeforeBudget 副本抖动对照（queue#86 判据）：
// claim 后**不执行**任务、直接把心跳置为过期再复位——修复前 n=5 的任务第 3 轮
// （floor(5/2)+1）就被判进死信；修复后预算按执行次数计，n-1 轮抖动不得提前
// 触顶，恰在第 n 轮才进终态。
func TestReplicaChurnDoesNotDeadLetterBeforeBudget(t *testing.T) {
	ctx := context.Background()
	const n = 5
	s := testStore(t, "runner-a").WithMaxAttempts(n)
	if err := s.Enqueue(ctx, "churn"); err != nil {
		t.Fatal(err)
	}
	for round := 1; round <= n-1; round++ {
		if !poisonCycle(t, ctx, s, "churn") {
			t.Fatalf("第 %d 轮应仍可领取（预算未耗尽）", round)
		}
		if st := statusOf(t, ctx, s, "churn"); st != StatusPending {
			t.Fatalf("副本抖动 %d 轮（预算 n=%d 未耗尽）不得进死信: %s", round, n, st)
		}
	}
	// 第 n 轮是预算内最后一次执行：此后复位即终态。
	if !poisonCycle(t, ctx, s, "churn") {
		t.Fatalf("第 %d 轮应领到任务（第 n 次执行）", n)
	}
	if st := statusOf(t, ctx, s, "churn"); st != StatusDeadLetter {
		t.Fatalf("执行满 n=%d 次后应进终态 dead_letter: %s", n, st)
	}
}
