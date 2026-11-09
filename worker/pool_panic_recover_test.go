package worker

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/oekit/logx"
)

// Pool.Start 构造期 panic 的可恢复性（oagentkit/queue#97）。
//
// 缺陷形态（两条，同根）：Start 用手工配对的 Lock/Unlock（无 defer），比值闸门
// panic 发生在 Unlock 之前 ⇒ 互斥量永久泄漏，此后 Start/Stop 全部卡死在
// p.mu.Lock()——优雅停机永不返回，进程僵死；且 p.started 已在闸门前置位，
// 源码注释承诺的「panic 后 Pool 未启动，修好字段还能再次 Start」两条都不成立。
//
// 修复后必须全部成立：panic 被捕获 → Stop 能返回 → 修好字段再 Start 真的
// 能拉起 worker 并处理任务（不是 already_started 的静默 no-op）。
func TestPoolStartPanicLeavesPoolUsable(t *testing.T) {
	q := newMockQueue("task-1")
	p := &Pool{
		Queue:             q,
		Run:               func(context.Context, string) error { return nil },
		N:                 1,
		HeartbeatInterval: 100 * time.Second, // ≥ StaleAfter ⇒ 构造期闸门 panic
		StaleAfter:        90 * time.Second,
		Log:               logx.NewSlogLogger("test"),
	}

	// [1] 构造期 panic 被调用方捕获（这正是闸门存在的意义：fail-fast）。
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("前置断言：比值闸门应 panic（HeartbeatInterval >= StaleAfter）")
			}
			if msg := fmt.Sprint(r); !contains(msg, "HeartbeatInterval") {
				t.Fatalf("panic 应点名配置项: %v", r)
			}
		}()
		p.Start(context.Background())
	}()

	// [2] panic 之后 Stop 必须能返回——修复前 p.mu 永久锁着，Stop 永不返回
	//（优雅停机被无限阻塞，配置错误退化成进程僵死）。
	stopped := make(chan struct{})
	go func() {
		p.Stop(time.Second)
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("闸门 panic 后 Stop 3s 内未返回——panic 泄漏 p.mu，Pool 永久僵死（oagentkit/queue#97）")
	}

	// [3] 修好字段后再次 Start 必须能拉起（Start 注释承诺的不变量）。修复前
	// 即使锁不泄漏，started 已在闸门前置位，再 Start 只会走 already_started
	// 分支静默 no-op。
	p.HeartbeatInterval = 5 * time.Millisecond
	p.StaleAfter = 50 * time.Millisecond
	started := make(chan struct{})
	go func() {
		p.Start(context.Background())
		close(started)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("修好字段后再次 Start 3s 内未返回——闸门 panic 后 Pool 不可复用（oagentkit/queue#97）")
	}

	// [4] 重启是真的：worker 真的在领取任务（证明 [3] 不是静默 no-op）。
	claimed := func() int {
		q.mu.Lock()
		defer q.mu.Unlock()
		return len(q.claimed)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && claimed() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if claimed() == 0 {
		t.Fatal("再次 Start 后 worker 未领取任何任务——Start 未真正生效")
	}

	p.Stop(2 * time.Second)
}
