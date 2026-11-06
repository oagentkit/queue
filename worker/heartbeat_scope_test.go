package worker

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oekit/logx"
)

// scopedQueue 实现 TaskQueue + 可选能力 ScopedHeartbeater，记录每次续期的
// 任务集与盲续次数（两条路径的调用面分开计数，才能证明「没走错分支」）。
//
// 刻意不挂在 mockQueue 上：panicQueue/errQueue 都嵌了 mockQueue，若 mockQueue
// 获得 TouchHeartbeatsFor，那些用例会静默改走 scoped 分支（空集早退），
// 于是它们以为在测盲续隔离、其实什么都没测。
type scopedQueue struct {
	mockQueue

	mu      sync.Mutex
	sets    [][]string
	scopedN int32
	blindN  int32
}

func (q *scopedQueue) TouchRunningHeartbeats(_ context.Context) error {
	atomic.AddInt32(&q.blindN, 1)
	return nil
}

func (q *scopedQueue) TouchHeartbeatsFor(_ context.Context, taskIDs []string) error {
	atomic.AddInt32(&q.scopedN, 1)
	q.mu.Lock()
	q.sets = append(q.sets, append([]string(nil), taskIDs...))
	q.mu.Unlock()
	return nil
}

func (q *scopedQueue) lastSet(t *testing.T) []string {
	t.Helper()
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.sets) == 0 {
		t.Fatal("TouchHeartbeatsFor 从未被调用")
	}
	return q.sets[len(q.sets)-1]
}

func (q *scopedQueue) setCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.sets)
}

func newScopedPool(q *scopedQueue, run func(context.Context, string) error) *Pool {
	return &Pool{Queue: q, Run: run, N: 1, Log: logx.NewSlogLogger("test")}
}

// waitUntil 事件驱动等待可观察条件成立（不猜时序，只轮询事实）。
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("超时：%s", what)
}

// TestHeartbeatRenewsOnlyInflight 心跳只续期本进程在飞的任务。
//
// 这是 #42 的判定核心：续期集里绝不能出现「归我但已退出」的任务，
// 否则它的心跳永远新鲜、ResetRunningToPending 永不命中。
func TestHeartbeatRenewsOnlyInflight(t *testing.T) {
	q := &scopedQueue{}
	p := newScopedPool(q, func(context.Context, string) error { return nil })

	p.trackStart("t1")
	p.trackStart("t2")
	p.trackDone("t2") // 模拟 t2 已退出（成功/报错/panic 收尾同一条路）
	p.heartbeat()

	got := q.lastSet(t)
	if len(got) != 1 || got[0] != "t1" {
		t.Fatalf("续期集 = %v, want [t1]（t2 已退出，必须移出）", got)
	}
	if n := atomic.LoadInt32(&q.blindN); n != 0 {
		t.Fatalf("实现 ScopedHeartbeater 时不得再走盲续: blindN=%d", n)
	}
}

// TestHeartbeatSkipsEmptySet 无在飞任务时**不发语句**。
//
// 「跳过」与「调一次空集」的区别不是性能：盲续的失效形态正是
// 「没事可续也发一条全量 UPDATE」，把它续活。
func TestHeartbeatSkipsEmptySet(t *testing.T) {
	q := &scopedQueue{}
	p := newScopedPool(q, func(context.Context, string) error { return nil })

	p.heartbeat()
	p.heartbeat()

	if n := q.setCount(); n != 0 {
		t.Fatalf("空集不得调用 TouchHeartbeatsFor: 调用 %d 次", n)
	}
	if n := atomic.LoadInt32(&q.blindN); n != 0 {
		t.Fatalf("不得退回盲续: blindN=%d", n)
	}
}

// TestHeartbeatFallsBackToBlind 未实现可选能力时行为完全不变（非破坏性）。
func TestHeartbeatFallsBackToBlind(t *testing.T) {
	q := newMockQueue()
	p := &Pool{Queue: q, Log: logx.NewSlogLogger("test")}

	// 在飞集对被追踪了也不影响：Queue 没这套能力，只能盲续
	p.trackStart("t1")
	p.heartbeat()
	p.trackDone("t1")

	if n := atomic.LoadInt32(&q.heartbeatCalls); n != 1 {
		t.Fatalf("应走盲续一次: heartbeatCalls=%d", n)
	}
}

// TestRunTrackedRegistersThenDeregisters 登记先于执行、注销无条件发生。
func TestRunTrackedRegistersThenDeregisters(t *testing.T) {
	q := &scopedQueue{}
	var seenDuringRun []string
	var p *Pool
	p = newScopedPool(q, func(context.Context, string) error {
		seenDuringRun = p.inflightSnapshot()
		return nil
	})

	p.runTracked(context.Background(), 0, "task-x")

	if len(seenDuringRun) != 1 || seenDuringRun[0] != "task-x" {
		t.Fatalf("Run 执行期间任务应在续期集内: %v", seenDuringRun)
	}
	if got := p.inflightSnapshot(); len(got) != 0 {
		t.Fatalf("Run 返回后任务必须移出续期集: %v", got)
	}
}

// TestRunTrackedDeregistersOnPanic panic 路径也必须注销——这是 #42 的主场景：
// 任务崩了就该尽快失联复位，而不是被自家心跳续成「还活着」。
func TestRunTrackedDeregistersOnPanic(t *testing.T) {
	q := &scopedQueue{}
	p := newScopedPool(q, func(context.Context, string) error { panic("task exploded") })

	p.runTracked(context.Background(), 0, "task-boom") // panic 已被隔离，不外泄

	if got := p.inflightSnapshot(); len(got) != 0 {
		t.Fatalf("panic 后任务必须移出续期集，否则 stale 复位永不命中: %v", got)
	}
}

// TestPoolHeartbeatDuringRunAndAfter 端到端：真跑 Pool，用阻塞 Run 把任务
// 钉在飞状态，验证心跳续的是它；放开后心跳不再续它。
func TestPoolHeartbeatDuringRunAndAfter(t *testing.T) {
	q := &scopedQueue{mockQueue: *newMockQueue("only-task")}
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once

	p := newScopedPool(q, func(context.Context, string) error {
		once.Do(func() { close(started) })
		<-release
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)
	defer p.Stop(2 * time.Second)

	<-started // 任务已在 Run 内执行

	p.heartbeat()
	if got := q.lastSet(t); len(got) != 1 || got[0] != "only-task" {
		t.Fatalf("在飞期间续期集 = %v, want [only-task]", got)
	}

	before := q.setCount()
	close(release)
	waitUntil(t, "任务应已移出续期集", func() bool { return len(p.inflightSnapshot()) == 0 })

	p.heartbeat()
	if after := q.setCount(); after != before {
		t.Fatalf("任务退出后心跳不得再续它: 调用次数 %d → %d", before, after)
	}
	if n := atomic.LoadInt32(&q.blindN); n != 0 {
		t.Fatalf("实现 ScopedHeartbeater 时不得走盲续: blindN=%d", n)
	}
}
