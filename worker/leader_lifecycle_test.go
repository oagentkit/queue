package worker

import (
	"context"
	"testing"
	"time"

	"github.com/oekit/logx"
)

// TestLeaderElectorStartAfterStopBeforeStart 「Start 之前误调一次 Stop」不得
// 让选主器静默砖化（queue#37）。
//
// 旧实现 Stop 无条件 close(e.stop)：startOnce 与 stop 一起被烧掉，之后 Start
// 的竞选 goroutine 首个 tick 即 stopping()==true 直接退出 —— 永不参选、
// IsLeader() 恒 false、无日志无错误。这与 Pool.Stop 的「未启动时 no-op，
// 真正的 Stop 依然有效」语义相反。
func TestLeaderElectorStartAfterStopBeforeStart(t *testing.T) {
	store := newMemLeaseStore()
	gained := make(chan struct{}, 1)
	e := NewLeaderElector(store, "poller", "A", time.Minute, 10*time.Millisecond,
		WithOnGained(func() {
			select {
			case gained <- struct{}{}:
			default:
			}
		}),
		WithLeaderLogger(logx.NewSlogLogger("test")))

	// 误调用：此时从未 Start。
	e.Stop()

	e.Start(context.Background())

	// 事件驱动：靠 onGained 收尾，不靠固定 sleep 猜首次 tick 何时完成。
	select {
	case <-gained:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop 先于 Start 之后，Start 未能参选成功——选主器被静默砖化（queue#37）")
	}
	if !e.IsLeader() {
		t.Fatal("当选后 IsLeader 应为 true")
	}

	// 之后真正的 Stop 依然有效：等到退出且租约已让出。
	e.Stop()
	if e.IsLeader() {
		t.Fatal("Stop 后 IsLeader 应为 false")
	}
	store.mu.Lock()
	holder, held := store.holder["poller"]
	store.mu.Unlock()
	if held {
		t.Fatalf("Stop 后租约应已让出，实得 holder=%q", holder)
	}
}

// TestLeaderElectorStopIdempotentAfterStart Start 之后的重复 Stop 仍是幂等：
// 已停机后再次 Stop 不得挂死在 <-e.done 上。
func TestLeaderElectorStopIdempotentAfterStart(t *testing.T) {
	store := newMemLeaseStore()
	e := NewLeaderElector(store, "poller", "A", time.Minute, 10*time.Millisecond,
		WithLeaderLogger(logx.NewSlogLogger("test")))
	e.Start(context.Background())

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !e.IsLeader() {
		time.Sleep(5 * time.Millisecond)
	}
	if !e.IsLeader() {
		t.Fatal("超时：未当选")
	}

	e.Stop()
	done := make(chan struct{})
	go func() {
		e.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("停机完成后再调 Stop 挂死")
	}
}
