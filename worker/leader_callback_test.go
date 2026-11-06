package worker

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oekit/logx"
)

// TestSlowCallbackDoesNotStallRenewal 慢回调不得顶住续约（queue#41）。
//
// 回调曾在 setLeader 内同步执行，而 setLeader 由 tick 调用——一个阻塞回调
// （等外部服务、做重活）就把下一次续约顶后。租约在回调执行期间过期，其他副本
// 接管，而本实例的 IsLeader 仍为 true：双主窗口从「≤一个轮询间隔」扩大成
// 「回调耗时 + 一个轮询间隔」。
//
// 判别方式：把回调卡住超过一个完整租期，然后让**别的持有者**去抢同一把锁。
// 续约还在跑 ⇒ 抢不到；续约被回调顶住 ⇒ 抢得到（旧实现）。
func TestSlowCallbackDoesNotStallRenewal(t *testing.T) {
	store := newMemLeaseStore()
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	const ttl = 200 * time.Millisecond

	a := NewLeaderElector(store, "poller", "A", ttl, 20*time.Millisecond,
		WithOnGained(func() {
			once.Do(func() { close(entered) })
			<-release // 模拟「回调做重活 / 等外部服务」
		}),
		WithLeaderLogger(logx.NewSlogLogger("test")))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Start(ctx)

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("超时：onGained 未触发")
	}

	// 卡住 3 个租期：旧实现（回调跑在竞选 goroutine 里）期间一次续约都发不出去。
	time.Sleep(3 * ttl)

	if !a.IsLeader() {
		t.Error("慢回调期间 IsLeader 不该翻成 false")
	}
	ok, err := store.TryAcquire(ctx, "poller", "B", ttl)
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	if ok {
		t.Fatal("回调阻塞期间租约被 B 抢走——续约被回调顶住了（queue#41）")
	}

	close(release)
	a.Stop()
}

// TestStopWaitsForCallbacksToDrain done 的含义包含「已入队的回调都跑过了」：
// Stop 返回后在飞回调必须已经完成，否则调用方读到的组件状态仍是旧的
// （「还没收到 onGained」），而它指望 Stop 保证的正是这件事。
//
// 这条不是 #41 的判别式（同步实现下它同样成立），它钉的是改异步之后
// **别把等待一起丢掉**。
func TestStopWaitsForCallbacksToDrain(t *testing.T) {
	store := newMemLeaseStore()
	var drained atomic.Bool

	e := NewLeaderElector(store, "poller", "A", time.Minute, 10*time.Millisecond,
		WithOnGained(func() {
			time.Sleep(150 * time.Millisecond) // 在飞回调
			drained.Store(true)
		}),
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
	if !drained.Load() {
		t.Fatal("Stop 返回时在飞回调尚未跑完——done 失去了「回调已排空」的含义")
	}
}

// TestCallbackPanicDoesNotKillDispatcher 回调 panic 只该留痕：调度器是唯一的
// 回调执行者，它死了就再也不会有任何角色翻转通知。
//
// 构造方式：onGained 故意 panic（证明 panic 被收编），随后让存储抖动使本实例
// 失主——若调度器还活着，onLost 会照常送达；否则这条断言永远等不到。
type flakyLeaseStore struct {
	memLeaseStore
	fail atomic.Bool
}

func (s *flakyLeaseStore) TryAcquire(ctx context.Context, key, holder string, ttl time.Duration) (bool, error) {
	if s.fail.Load() {
		return false, errors.New("存储抖动（用例故意）")
	}
	return s.memLeaseStore.TryAcquire(ctx, key, holder, ttl)
}

func TestCallbackPanicDoesNotKillDispatcher(t *testing.T) {
	store := &flakyLeaseStore{memLeaseStore: *newMemLeaseStore()}
	var gained, lost atomic.Int32

	e := NewLeaderElector(store, "poller", "A", time.Minute, 10*time.Millisecond,
		WithOnGained(func() {
			gained.Add(1)
			panic("boom") // 用例故意炸
		}),
		WithOnLost(func() { lost.Add(1) }),
		WithLeaderLogger(logx.NewSlogLogger("test")))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.Start(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && gained.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if gained.Load() == 0 {
		t.Fatal("超时：onGained 未触发")
	}

	// 让续约开始失败 ⇒ setLeader(false) ⇒ onLost 入队。
	store.fail.Store(true)

	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && lost.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if lost.Load() == 0 {
		t.Fatal("超时：失主后 onLost 未送达——调度器已被上一次回调的 panic 打死")
	}
	e.Stop()
}
