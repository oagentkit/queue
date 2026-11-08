package worker

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oekit/logx"
)

// Pool 的心跳参数配置面（oagentkit/queue#88）。
//
// 失效形态：TaskQueue doc 宣称「周期由宿主经 Pool 字段配置」，但 Pool 没有那
// 两个字段——心跳周期是包内未导出 var（进程全局、无锁可变、两个 Pool 无法
// 各配一份），staleAfter 是 const 直接传下去。文档自定的两条红线宿主都遵守
// 不了：缺省比值 90s/20s=4.5× 低于文档要求的 ≥5×；单任务正常执行时长可能
// 超过 90s 的宿主没有任何手段调大 staleAfter。失效是静默的：配不上不报错，
// 只在某次心跳轮次错过时以「同一任务双跑」的形式爆出来。

// staleCaptureQueue 记录 ResetRunningToPending 收到的 staleAfter 值——
// 只断言「被调用了」证不出配置生效，必须断言收到的值。
type staleCaptureQueue struct {
	mu     sync.Mutex
	stales []time.Duration
}

func (q *staleCaptureQueue) ClaimNextPending(context.Context) (string, bool, error) {
	return "", false, nil
}

func (q *staleCaptureQueue) TouchRunningHeartbeats(context.Context) error { return nil }

func (q *staleCaptureQueue) ResetRunningToPending(_ context.Context, staleAfter time.Duration) (int64, error) {
	q.mu.Lock()
	q.stales = append(q.stales, staleAfter)
	q.mu.Unlock()
	return 0, nil
}

func (q *staleCaptureQueue) snapshot() []time.Duration {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]time.Duration, len(q.stales))
	copy(out, q.stales)
	return out
}

// TestPoolStaleAfterFieldReachesReset 运行期判据：把两个值压到毫秒级，
// 断言 ResetRunningToPending 收到的 staleAfter **等于配置值**（不是 90s），
// 且心跳周期由 HeartbeatInterval 字段驱动。
func TestPoolStaleAfterFieldReachesReset(t *testing.T) {
	q := &staleCaptureQueue{}
	p := &Pool{
		Queue:             q,
		Run:               func(context.Context, string) error { return nil },
		N:                 1,
		HeartbeatInterval: 5 * time.Millisecond,
		StaleAfter:        50 * time.Millisecond,
	}
	p.Start(context.Background())
	time.Sleep(200 * time.Millisecond)
	p.Stop(2 * time.Second)

	got := q.snapshot()
	if len(got) == 0 {
		t.Fatal("心跳轮次应已触发 ResetRunningToPending")
	}
	for i, d := range got {
		if d != 50*time.Millisecond {
			t.Fatalf("第 %d 次 ResetRunningToPending 收到 staleAfter=%v, want 50ms——配置未生效（仍是缺省 90s？）", i+1, d)
		}
	}
}

// TestPoolHeartbeatIntervalFieldDrivesLoop 心跳周期字段真的驱动心跳循环：
// 5ms 周期下 200ms 内应有远多于 2 轮心跳（缺省 15s 只会有 0 轮）。
func TestPoolHeartbeatIntervalFieldDrivesLoop(t *testing.T) {
	q := newMockQueue()
	p := &Pool{
		Queue:             q,
		Run:               func(context.Context, string) error { return nil },
		N:                 1,
		HeartbeatInterval: 5 * time.Millisecond,
	}
	p.Start(context.Background())
	time.Sleep(200 * time.Millisecond)
	p.Stop(2 * time.Second)

	if got := atomic.LoadInt32(&q.heartbeatCalls); got < 2 {
		t.Fatalf("HeartbeatInterval=5ms 时 200ms 内应有多轮心跳，实得 %d——字段未驱动循环", got)
	}
}

// TestPoolDefaultStaleAfterIsStaleRunningAfter 缺省判据（兼容性红线）：不设
// 字段时 resetStale 收到的必须是 StaleRunningAfter（90s）——防止把缺省改成
// 一个新数字而无人知。直调 resetStale（不经 Start 的 15s 心跳等待）。
func TestPoolDefaultStaleAfterIsStaleRunningAfter(t *testing.T) {
	q := &staleCaptureQueue{}
	p := &Pool{Queue: q}
	p.resetStale()
	got := q.snapshot()
	if len(got) != 1 || got[0] != StaleRunningAfter {
		t.Fatalf("缺省 staleAfter = %v, want %v", got, StaleRunningAfter)
	}
	// 成对对照：显式配置直调同样生效（归一发生在读点，不依赖 Start）。
	q2 := &staleCaptureQueue{}
	p2 := &Pool{Queue: q2, StaleAfter: 42 * time.Millisecond}
	p2.resetStale()
	got2 := q2.snapshot()
	if len(got2) != 1 || got2[0] != 42*time.Millisecond {
		t.Fatalf("配置的 staleAfter = %v, want 42ms", got2)
	}
}

// TestPoolRejectsHeartbeatNotBelowStale 构造期闸门：HeartbeatInterval >=
// StaleAfter 必须 panic——与 NewLeaderElector 对 interval >= ttl 的处理同一道
// 纪律（心跳周期不短于失联阈值时，运行中的任务在两次心跳之间必然被判失联）。
func TestPoolRejectsHeartbeatNotBelowStale(t *testing.T) {
	cases := []struct {
		name  string
		hb    time.Duration
		stale time.Duration
	}{
		{"both set equal", 50 * time.Millisecond, 50 * time.Millisecond},
		{"hb exceeds stale", time.Second, 50 * time.Millisecond},
		{"hb exceeds default stale", 100 * time.Second, 0},
		{"default hb exceeds set stale", 0, time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			func() {
				defer func() {
					r := recover()
					if r == nil {
						t.Fatalf("HeartbeatInterval=%v StaleAfter=%v 应构造期 panic", tc.hb, tc.stale)
					}
					msg := fmt.Sprint(r)
					if !contains(msg, "HeartbeatInterval") || !contains(msg, "StaleAfter") {
						t.Fatalf("panic 应点名两个参数: %v", r)
					}
				}()
				p := &Pool{
					Queue:             newMockQueue(),
					Run:               func(context.Context, string) error { return nil },
					HeartbeatInterval: tc.hb,
					StaleAfter:        tc.stale,
				}
				p.Start(context.Background())
			}()
		})
	}
}

// TestPoolAcceptsPairedHeartbeatStale 成对对照：HeartbeatInterval*5 <=
// StaleAfter 的配置必须放行（量化建议 ≥5×），缺省字段同样放行。
func TestPoolAcceptsPairedHeartbeatStale(t *testing.T) {
	h := &captureHandler{}
	p := &Pool{
		Queue:             newMockQueue(),
		Run:               func(context.Context, string) error { return nil },
		N:                 1,
		Log:               logx.NewSlogLogger("test", logx.WithHandler(h)),
		HeartbeatInterval: 5 * time.Millisecond,
		StaleAfter:        50 * time.Millisecond, // 恰 10×，≥5× 放行
	}
	p.Start(context.Background())
	p.Stop(2 * time.Second)
	if _, ok := h.find("started"); !ok {
		t.Fatalf("成对配置应正常启动，实际日志: %v", h.messages())
	}

	// 缺省（0/0）：15s < 90s，放行。快速起停验证闸门不误伤缺省路径。
	p2 := &Pool{
		Queue: newMockQueue(),
		Run:   func(context.Context, string) error { return nil },
		N:     1,
	}
	p2.Start(context.Background())
	p2.Stop(2 * time.Second)
}

// TestDefaultRatioMeetsDocRecommendation 缺省比值必须满足文档自定的量化建议
// （≥5×，queue#88）：防止未来把缺省改回去而文档红线无人再对得上。
func TestDefaultRatioMeetsDocRecommendation(t *testing.T) {
	if HeartbeatInterval*5 > StaleRunningAfter {
		t.Fatalf("缺省比值 %s/%s = %.2f× 低于文档建议的 ≥5×",
			StaleRunningAfter, HeartbeatInterval,
			float64(StaleRunningAfter)/float64(HeartbeatInterval))
	}
}
