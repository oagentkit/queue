package worker

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oekit/logx"
)

// mockQueue 实现 TaskQueue 接口用于测试。
type mockQueue struct {
	mu             sync.Mutex
	pending        []string
	claimed        []string
	heartbeatCalls int32
}

func newMockQueue(taskIDs ...string) *mockQueue {
	return &mockQueue{pending: taskIDs}
}

func (q *mockQueue) ClaimNextPending(_ context.Context) (string, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) == 0 {
		return "", false, nil
	}
	id := q.pending[0]
	q.pending = q.pending[1:]
	q.claimed = append(q.claimed, id)
	return id, true, nil
}

func (q *mockQueue) TouchRunningHeartbeats(_ context.Context) error {
	atomic.AddInt32(&q.heartbeatCalls, 1)
	return nil
}

func (q *mockQueue) ResetRunningToPending(_ context.Context, _ time.Duration) (int64, error) {
	return 0, nil
}

func TestPoolClaimsAndRuns(t *testing.T) {
	q := newMockQueue("task-1", "task-2", "task-3")
	var mu sync.Mutex
	var ran []string

	p := &Pool{
		Queue: q,
		Run: func(_ context.Context, taskID string) error {
			mu.Lock()
			ran = append(ran, taskID)
			mu.Unlock()
			return nil
		},
		N:   1,
		Log: logx.NewSlogLogger("test"),
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)

	// 等待所有任务执行完
	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		done := len(ran)
		mu.Unlock()
		if done >= 3 {
			break
		}
		select {
		case <-deadline:
			cancel()
			t.Fatalf("超时：只完成了 %d/3 个任务", done)
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}

	cancel()
	p.Stop(time.Second)

	mu.Lock()
	defer mu.Unlock()
	if len(ran) != 3 {
		t.Fatalf("应完成 3 个任务，实际 %d", len(ran))
	}
}

func TestPoolMultipleWorkers(t *testing.T) {
	// 5 个任务，3 个 worker
	ids := make([]string, 5)
	for i := range ids {
		ids[i] = "task-" + string(rune('A'+i))
	}
	q := newMockQueue(ids...)
	var count atomic.Int32

	p := &Pool{
		Queue: q,
		Run: func(_ context.Context, _ string) error {
			time.Sleep(50 * time.Millisecond)
			count.Add(1)
			return nil
		},
		N:   3,
		Log: logx.NewSlogLogger("test"),
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)

	deadline := time.After(5 * time.Second)
	for count.Load() < 5 {
		select {
		case <-deadline:
			cancel()
			t.Fatalf("超时：完成 %d/5", count.Load())
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}

	cancel()
	p.Stop(time.Second)
}

func TestPoolStopGraceful(t *testing.T) {
	q := newMockQueue("task-1")
	started := make(chan struct{})
	var done atomic.Bool

	p := &Pool{
		Queue: q,
		Run: func(ctx context.Context, _ string) error {
			close(started)
			select {
			case <-ctx.Done():
				done.Store(true)
			case <-time.After(10 * time.Second):
				done.Store(true)
			}
			return nil
		},
		N:   1,
		Log: logx.NewSlogLogger("test"),
	}
	ctx := context.Background()
	p.Start(ctx)

	<-started
	// 短 grace：不应取消（任务自然完成前 grace 到期）
	p.Stop(10 * time.Millisecond)

	// 硬取消后任务应收到 ctx 取消
	time.Sleep(100 * time.Millisecond)
	if !done.Load() {
		t.Fatal("Stop 应最终取消在途任务")
	}
}

func TestPoolEmptyQueue(t *testing.T) {
	q := newMockQueue() // 空队列
	var count atomic.Int32

	p := &Pool{
		Queue: q,
		Run: func(_ context.Context, _ string) error {
			count.Add(1)
			return nil
		},
		N:   1,
		Log: logx.NewSlogLogger("test"),
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)

	time.Sleep(200 * time.Millisecond) // 空转一段时间
	cancel()
	p.Stop(time.Second)

	if count.Load() != 0 {
		t.Fatal("空队列不应执行任何任务")
	}
}

func TestPoolZeroWorkersDefaults(t *testing.T) {
	q := newMockQueue("task-1")
	p := &Pool{
		Queue: q,
		Run:   func(_ context.Context, _ string) error { return nil },
		N:     0, // 应默认为 1
		Log:   logx.NewSlogLogger("test"),
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)
	cancel()
	p.Stop(time.Second)
	// 不应 panic
}

func TestStopBeforeStartDoesNotBurnShutdown(t *testing.T) {
	// 回归：先 Stop（未启动）后 Start，真正的 Stop 必须仍然有效
	//（此前 sync.Once 被 Stop-before-Start 烧穿，之后停机永远失效）
	q := newMockQueue("t1")
	var ran atomic.Int32
	p := &Pool{Queue: q, N: 1, Log: logx.NewSlogLogger("test"),
		Run: func(ctx context.Context, taskID string) error {
			ran.Add(1)
			return nil
		}}

	p.Stop(time.Second) // 未启动时误调用
	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)
	defer cancel()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && ran.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if ran.Load() == 0 {
		t.Fatal("任务应被执行")
	}

	p.Stop(2 * time.Second) // 真正的停机
	select {
	case <-p.done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop 应在 Start 之后正常生效（done 关闭）")
	}
}

func TestTaskPanicDoesNotKillWorker(t *testing.T) {
	// 回归：任务 panic 只损失该任务，worker 存活继续拉取（池不得静默减员）
	var mu sync.Mutex
	q := newMockQueue("panic-1", "ok-1", "ok-2")
	var done []string
	p := &Pool{Queue: q, N: 1, Log: logx.NewSlogLogger("test"),
		Run: func(ctx context.Context, taskID string) error {
			if strings.HasPrefix(taskID, "panic") {
				panic("boom")
			}
			mu.Lock()
			done = append(done, taskID)
			mu.Unlock()
			return nil
		}}

	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(done)
		mu.Unlock()
		if n == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	p.Stop(2 * time.Second)

	mu.Lock()
	defer mu.Unlock()
	if len(done) != 2 {
		t.Fatalf("panic 后 worker 应继续消费后续任务: %v", done)
	}
}

// panicQueue 队列方法直接 panic——锁死 guardedCall 的隔离纪律：
// Queue 是调用方实现（GORM 等），panic 若穿透会杀死心跳 goroutine →
// 本实例全部在途任务心跳停止 → 90s 后被对端复位重跑（静默双跑）。
type panicQueue struct{ mockQueue }

func (q *panicQueue) TouchRunningHeartbeats(_ context.Context) error {
	panic("queue backend exploded")
}

func (q *panicQueue) ResetRunningToPending(_ context.Context, _ time.Duration) (int64, error) {
	panic("reset exploded")
}

func TestGuardedCallIsolatesQueuePanic(t *testing.T) {
	p := &Pool{Queue: &panicQueue{}, Log: logx.NewSlogLogger("test")}

	// panic 被隔离为 error，不穿透调用方
	if err := p.guardedCall("test.panic", "test.fail",
		func(context.Context) error { panic("fn exploded") }); err == nil ||
		!strings.Contains(err.Error(), "panic") {
		t.Fatalf("panic 应转为 error: %v", err)
	}
	// 队列方法 panic 同样被骨架隔离（heartbeat/resetStale 无返回值——
	// 不 panic 即为隔离成功）
	p.heartbeat()
	p.resetStale()
}

// errQueue 队列返回错误——guardedCall 透传错误（不吞）。
type errQueue struct{ mockQueue }

func (q *errQueue) TouchRunningHeartbeats(_ context.Context) error {
	return context.DeadlineExceeded
}

func TestGuardedCallPropagatesError(t *testing.T) {
	p := &Pool{Queue: &errQueue{}, Log: logx.NewSlogLogger("test")}
	err := p.guardedCall("test.panic", "test.fail",
		func(context.Context) error { return context.DeadlineExceeded })
	if err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("队列错误应透传: %v", err)
	}
}
