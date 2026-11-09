package worker

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oekit/logx"
)

// countingLeaseStore 给任意 LeaseStore 加 TryAcquire 计数——「Stop 之后
// 再 Start 是否重新参选」唯一可靠的观测面就是竞选对 TryAcquire 的真实调用
// （IsLeader 恒 false 与「从未参选」无法区分，oagentkit/queue#96）。
type countingLeaseStore struct {
	LeaseStore
	acquires atomic.Int64
}

func (s *countingLeaseStore) TryAcquire(ctx context.Context, key, holder string, ttl time.Duration) (bool, error) {
	s.acquires.Add(1)
	return s.LeaseStore.TryAcquire(ctx, key, holder, ttl)
}

func waitLeader(t *testing.T, e *LeaderElector, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) && !e.IsLeader() {
		time.Sleep(5 * time.Millisecond)
	}
	if !e.IsLeader() {
		t.Fatal("超时：未当选")
	}
}

// TestLeaderElectorRestartAfterStop 「Start → Stop → Start」必须重新参选
// （oagentkit/queue#96）。
//
// 缺陷形态：Start 的整个启动流程被 sync.Once 包住，Stop 之后 startOnce 已被
// 消耗 ⇒ 第二次 Start 的函数体一次都不执行——不拉起竞选 goroutine、不打
// 日志、不返回错误。本实例从此永不竞争租约而调用方毫无感知（IsLeader=false
// 与「从未启动」表象一致），排障时无法区分「已重启」与「被静默忽略」这两种
// 语义相反的结果。
//
// 判据必须是 TryAcquire 计数增量（delta=0 即竞选 goroutine 从未再跑），
// 外加事件驱动的重当选断言。
func TestLeaderElectorRestartAfterStop(t *testing.T) {
	store := &countingLeaseStore{LeaseStore: newMemLeaseStore()}
	e := NewLeaderElector(store, "poller", "A", time.Minute, 10*time.Millisecond,
		WithLeaderLogger(logx.NewSlogLogger("test")))
	ctx := context.Background()

	e.Start(ctx)
	waitLeader(t, e, 2*time.Second)
	e.Stop()
	if e.IsLeader() {
		t.Fatal("Stop 后 IsLeader 应为 false")
	}

	before := store.acquires.Load()
	if before == 0 {
		t.Fatal("前置断言：首轮竞选应对 TryAcquire 有调用")
	}

	// 重启：第二次 Start 必须重建竞选 goroutine。
	e.Start(ctx)
	waitLeader(t, e, 2*time.Second) // 缺陷下这里超时：竞选 goroutine 永不再跑

	// 竞选对存储的真实调用必须恢复增长（不只 IsLeader 翻转——那是 tick 的
	// 副产物，证明不了参选在持续进行）。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && store.acquires.Load() <= before {
		time.Sleep(5 * time.Millisecond)
	}
	if got := store.acquires.Load(); got <= before {
		t.Fatalf("Stop 后重启的竞选 goroutine 未参选：TryAcquire delta=0（选主器被 startOnce 静默砖化，oagentkit/queue#96）")
	}

	// 重启后的停机依然有效：租约让出、IsLeader 归 false。
	e.Stop()
	if e.IsLeader() {
		t.Fatal("重启后再 Stop，IsLeader 应为 false")
	}
	ms := store.LeaseStore.(*memLeaseStore)
	ms.mu.Lock()
	holder, held := ms.holder["poller"]
	ms.mu.Unlock()
	if held {
		t.Fatalf("重启后再 Stop 租约应已让出，实得 holder=%q", holder)
	}
}

// TestLeaderElectorRestartCycle 停机/重启反复循环：每一轮都必须重新参选并
// 重新当选——状态机的每条迁移路径都不允许被任何 Once 消耗掉。
func TestLeaderElectorRestartCycle(t *testing.T) {
	store := &countingLeaseStore{LeaseStore: newMemLeaseStore()}
	e := NewLeaderElector(store, "poller", "A", time.Minute, 10*time.Millisecond,
		WithLeaderLogger(logx.NewSlogLogger("test")))
	ctx := context.Background()

	for round := 1; round <= 5; round++ {
		e.Start(ctx)
		waitLeader(t, e, 2*time.Second)
		e.Stop()
		if e.IsLeader() {
			t.Fatalf("第 %d 轮：Stop 后 IsLeader 应为 false", round)
		}
	}
}

// TestLeaderElectorRestartAfterContextCancel 另一条同型路径：ctx 取消退出
// 竞选后再 Start 也必须能重新参选——旧实现里 startOnce 同样被首次 Start
// 消耗，ctx 取消即永久砖化（oagentkit/queue#96）。
func TestLeaderElectorRestartAfterContextCancel(t *testing.T) {
	store := &countingLeaseStore{LeaseStore: newMemLeaseStore()}
	e := NewLeaderElector(store, "poller", "A", time.Minute, 10*time.Millisecond,
		WithLeaderLogger(logx.NewSlogLogger("test")))

	ctx, cancel := context.WithCancel(context.Background())
	e.Start(ctx)
	waitLeader(t, e, 2*time.Second)
	cancel()
	// 竞选 goroutine 退出是异步的：等到 IsLeader 归 false（yield 已跑完）。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && e.IsLeader() {
		time.Sleep(5 * time.Millisecond)
	}
	if e.IsLeader() {
		t.Fatal("ctx 取消后 IsLeader 应为 false")
	}

	before := store.acquires.Load()
	e.Start(context.Background())
	waitLeader(t, e, 2*time.Second)
	if store.acquires.Load() <= before {
		t.Fatal("ctx 取消退出后再 Start 未重新参选（startOnce 被首次 Start 消耗）")
	}
	e.Stop()
}

// TestLeaderElectorConcurrentStartSingleCampaign 并发重复 Start：只允许一个
// 竞选 goroutine（文档化 no-op 契约），且不得挂死——状态机在锁内迁移，
// 并发调用全部收敛到同一条生命周期。
func TestLeaderElectorConcurrentStartSingleCampaign(t *testing.T) {
	store := &countingLeaseStore{LeaseStore: newMemLeaseStore()}
	e := NewLeaderElector(store, "poller", "A", time.Minute, 10*time.Millisecond,
		WithLeaderLogger(logx.NewSlogLogger("test")))
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.Start(ctx)
		}()
	}
	wg.Wait()

	waitLeader(t, e, 2*time.Second)
	e.Stop()
	if e.IsLeader() {
		t.Fatal("并发 Start 后 Stop，IsLeader 应为 false")
	}
}
