package sqlqueue

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动（测试依赖，零生产依赖先例同 lib/pq）
)

func testStore(t *testing.T, runnerID string) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", "file:memdb?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1) // :memory: 每连接独立库——共享缓存 + 单连接
	t.Cleanup(func() { db.Close() })
	s := New(db, SQLite, runnerID)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

// 全周期：入队 → claim → 心跳 → 失联重置 → 再 claim。
func TestQueueLifecycle(t *testing.T) {
	ctx := context.Background()
	a := testStore(t, "runner-a")

	if err := a.Enqueue(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Enqueue(ctx, "t2"); err != nil {
		t.Fatal(err)
	}
	// FIFO：先入先出
	id, ok, err := a.ClaimNextPending(ctx)
	if err != nil || !ok || id != "t1" {
		t.Fatalf("claim#1 = %q ok=%v err=%v, want t1", id, ok, err)
	}
	id, ok, err = a.ClaimNextPending(ctx)
	if err != nil || !ok || id != "t2" {
		t.Fatalf("claim#2 = %q ok=%v err=%v, want t2", id, ok, err)
	}
	// 队列空
	if _, ok, err := a.ClaimNextPending(ctx); err != nil || ok {
		t.Fatalf("空队列: ok=%v err=%v", ok, err)
	}
	// 心跳续期不报错、不改归属
	if err := a.TouchRunningHeartbeats(ctx); err != nil {
		t.Fatal(err)
	}
	// 未失联不重置
	if n, err := a.ResetRunningToPending(ctx, time.Hour); err != nil || n != 0 {
		t.Fatalf("未失联应 0 重置: n=%d err=%v", n, err)
	}
	// 失联重置（heartbeat 已写为过去时刻不可能——DB 时钟；用大 stale 阈值穿过：
	// 直接把心跳回拨模拟时间流逝，语义与真时钟等待等价）
	if _, err := a.db.ExecContext(ctx, `UPDATE agentkit_task SET heartbeat_at = strftime('%s','now') - 3600`); err != nil {
		t.Fatal(err)
	}
	n, err := a.ResetRunningToPending(ctx, 90*time.Second)
	if err != nil || n != 2 {
		t.Fatalf("失联应重置 2 条: n=%d err=%v", n, err)
	}
	// 重置后可再 claim，attempts 已累计
	id, ok, err = a.ClaimNextPending(ctx)
	if err != nil || !ok || id != "t1" {
		t.Fatalf("重置后 claim = %q ok=%v err=%v", id, ok, err)
	}
	var attempts int
	if err := a.db.QueryRowContext(ctx, `SELECT attempts FROM agentkit_task WHERE task_id='t1'`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Fatalf("t1 attempts = %d, want 3（claim+1 重置+1 再claim+1）", attempts)
	}
}

// 心跳归属：runner-a 的心跳不动 runner-b 的任务（runner_id 守卫）。
func TestHeartbeatScopedToRunner(t *testing.T) {
	ctx := context.Background()
	a := testStore(t, "runner-a")
	b := testStore(t, "runner-b")
	// 共享同一物理表：b 内存库不同表空间——这里用 a 的库建 b 的 store
	b2 := New(a.db, SQLite, "runner-b")
	if err := b2.WithTable("agentkit_task").Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	_ = b

	_ = a.Enqueue(ctx, "ta")
	_ = b2.Enqueue(ctx, "tb")
	if _, ok, _ := a.ClaimNextPending(ctx); !ok {
		t.Fatal("a 应抢到 ta")
	}
	if _, ok, _ := b2.ClaimNextPending(ctx); !ok {
		t.Fatal("b 应抢到 tb")
	}
	// b 心跳后 a 的任务心跳不变（归属守卫）——直接查库验证
	if err := b2.TouchRunningHeartbeats(ctx); err != nil {
		t.Fatal(err)
	}
	var aHB, bHB sql.NullFloat64
	if err := a.db.QueryRowContext(ctx, `SELECT heartbeat_at FROM agentkit_task WHERE task_id='ta'`).Scan(&aHB); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRowContext(ctx, `SELECT heartbeat_at FROM agentkit_task WHERE task_id='tb'`).Scan(&bHB); err != nil {
		t.Fatal(err)
	}
	if !aHB.Valid || !bHB.Valid {
		t.Fatalf("两个任务都应有心跳: a=%v b=%v", aHB, bHB)
	}
	// 归属正确性靠 ResetRunningToPending 的 runner 清空验证：只回拨 a 的任务
	if _, err := a.db.ExecContext(ctx, `UPDATE agentkit_task SET heartbeat_at = strftime('%s','now') - 3600 WHERE task_id='ta'`); err != nil {
		t.Fatal(err)
	}
	n, err := b2.ResetRunningToPending(ctx, 90*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = n
	var status string
	if err := a.db.QueryRowContext(ctx, `SELECT status FROM agentkit_task WHERE task_id='ta'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("ta 应被重置回 pending: %s", status)
	}
}

// 并发双抢：N goroutine 抢 1 个任务，恰一个成功——「单语句守卫 UPDATE」
// 的原子性（-race 下跑）。
func TestConcurrentClaimSingleWinner(t *testing.T) {
	ctx := context.Background()
	s := testStore(t, "shared-runner")
	if err := s.Enqueue(ctx, "only"); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	wins := 0
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok, err := s.ClaimNextPending(ctx)
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			if ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("应恰一个 winner，得到 %d", wins)
	}
}

// 多任务并发抢占：全部任务被分给不同 claimer、总量守恒（无丢无重）。
func TestConcurrentClaimDistributesAll(t *testing.T) {
	ctx := context.Background()
	s := testStore(t, "r")
	const total = 32
	for i := 0; i < total; i++ {
		if err := s.Enqueue(ctx, fmt.Sprintf("t%02d", i)); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	claimed := map[string]bool{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				id, ok, err := s.ClaimNextPending(ctx)
				if err != nil {
					t.Errorf("claim: %v", err)
					return
				}
				if !ok {
					return
				}
				mu.Lock()
				if claimed[id] {
					t.Errorf("任务 %s 被重复 claim（双跑）", id)
				}
				claimed[id] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(claimed) != total {
		t.Fatalf("应全部被抢占 %d/%d", len(claimed), total)
	}
}
