package sqlqueue

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ClaimNextPendingDetailed 返回的 attempts 必须是本次 claim 真实落库的值
// （oagentkit/queue#98）。
//
// 缺陷形态：旧实现先 SELECT 快照 attempts（seen）再发守卫 UPDATE，返回
// seen+1——这要求「SELECT 与 UPDATE 之间该行 attempts 不变」。但
// ResetRunningToPending 也做 attempts+1 并把 running 行改回 pending：并发
// 「他副本 claim → 复位」的复合序列恰好落进 SELECT→UPDATE 窗口时，本调用的
// 守卫 UPDATE 仍然命中（行又被复位成 pending 了），返回值却比库内实际值
// 少 2。宿主的毒任务闸门据此系统性偏低报、晚触发。
//
// 判据（对同一行的 reported 序列必须**严格递增**）：库内 attempts 只增不减
// （claim 与复位各 +1，本测试不重置计数），修复后每次 claim 的返回值都取自
// 守卫 UPDATE 自身（RETURNING / 锁内读），天然严格递增；缺陷下 SELECT 快照
// 可以早于上一次 claim 的写入，序列出现持平甚至倒退——任何一次非严格递增
// 都是「返回值 ≠ 落库值」的实锤。正向观测（读到比返回值大的库值）可能是
// 返回之后的合法并发写，不作数；只有这条不变量是可靠的判据。
func TestClaimAttemptsMatchesPersistedValueUnderResetRace(t *testing.T) {
	ctx := context.Background()
	s := testStore(t, "stress-runner")

	const tasks = 4
	for i := range tasks {
		if err := s.Enqueue(ctx, fmt.Sprintf("poison-%d", i)); err != nil {
			t.Fatal(err)
		}
	}

	// lastReported[rowID] 记录该行上一次 claim 的返回值；并发读写走 mutex。
	var mu sync.Mutex
	lastReported := make(map[string]int)
	var violations atomic.Int64
	record := func(row string, reported int) {
		mu.Lock()
		defer mu.Unlock()
		if prev, ok := lastReported[row]; ok && reported <= prev {
			violations.Add(1)
		}
		lastReported[row] = reported
	}

	// stop 之后的检查放在 wg.Wait 后，goroutine 内不得调 t.Fatal（-race 与
	// testing 规范：非测试 goroutine 直接 FailNow 是未定义行为）。
	stop := make(chan struct{})
	var wg sync.WaitGroup

	// 心跳回拨：让 running 行随时处于「失联」状态——SQLite nowFn 是整秒
	// 粒度，不回拨时复位只在秒边界成批触发，竞态窗口命中率过低。回拨方与
	// 复位方解耦，复位即可常数命中。
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = s.db.ExecContext(ctx,
				`UPDATE agentkit_task SET heartbeat_at = heartbeat_at - 3600 WHERE status = 'running'`)
		}
	})

	// 复位方 ×2：把失联 running 行改回 pending（attempts+1）。
	for range 2 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = s.ResetRunningToPending(ctx, 0)
			}
		})
	}

	// 领取方 ×4：记录 reported 序列，断言严格递增。
	const claimers = 4
	var claims atomic.Int64
	for range claimers {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				id, reported, ok, err := s.ClaimNextPendingDetailed(ctx)
				if err != nil {
					continue // 存储抖动不构成判据，继续压
				}
				if !ok {
					continue
				}
				claims.Add(1)
				record(id, reported)
			}
		})
	}

	// 压测窗口：至少跑到出现足量 claim（正常路径每秒数千次），上限 5s。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && claims.Load() < 4000 {
		time.Sleep(10 * time.Millisecond)
	}
	close(stop)
	wg.Wait()

	if got := claims.Load(); got < 500 {
		t.Fatalf("压测未充分运行（仅 %d 次 claim），结果不可判——环境过慢", got)
	}
	if v := violations.Load(); v > 0 {
		t.Fatalf("reported attempts 序列出现 %d 次非严格递增——ClaimNextPendingDetailed 返回的不是真实落库值（SELECT 快照与守卫 UPDATE 之间被并发复位改写，oagentkit/queue#98）", v)
	}
}

// TestClaimAttemptsSequentialExactness 对照组（单线程精确性）：claim 与失联
// 复位顺序交错时，返回值必须逐次等于库内实际值——RETURNING 改造不得改变
// 顺序语义（attempts 含本次领取：1 → 复位+1 → 3 → …）。
func TestClaimAttemptsSequentialExactness(t *testing.T) {
	ctx := context.Background()
	s := testStore(t, "seq-runner")
	if err := s.Enqueue(ctx, "task-1"); err != nil {
		t.Fatal(err)
	}
	// 计数口径（queue#70 既有契约）：claim 与失联复位各 +1——第 i 轮 claim 的
	// 期望值是 2i-1（claim、reset、claim、reset… 交错累计）。
	for round := 1; round <= 4; round++ {
		want := 2*round - 1
		id, reported, ok, err := s.ClaimNextPendingDetailed(ctx)
		if err != nil || !ok || id != "task-1" {
			t.Fatalf("claim#%d = %q ok=%v err=%v", round, id, ok, err)
		}
		var persisted int
		if err := s.db.QueryRowContext(ctx,
			`SELECT attempts FROM agentkit_task WHERE task_id='task-1'`).Scan(&persisted); err != nil {
			t.Fatal(err)
		}
		if reported != persisted || reported != want {
			t.Fatalf("claim#%d：reported=%d persisted=%d want=%d——返回值与落库值不一致",
				round, reported, persisted, want)
		}
		// 心跳冻结 → 复位（attempts+1）→ 下一轮 claim 的期望值 +2。
		if _, err := s.db.ExecContext(ctx,
			`UPDATE agentkit_task SET heartbeat_at = strftime('%s','now') - 3600 WHERE task_id='task-1'`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ResetRunningToPending(ctx, 90*time.Second); err != nil {
			t.Fatal(err)
		}
	}
}
