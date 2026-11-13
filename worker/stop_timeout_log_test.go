package worker

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oekit/logx"
)

// captureHandler 收 slog 记录（logx.WithHandler 注入点），用于断言「哪条日志
// 打了、带了什么字段」——静默失效的修复必须能被日志本身证伪。
type captureHandler struct {
	mu   sync.Mutex
	recs []captureRec
}

type captureRec struct {
	level slog.Level
	msg   string
	kv    map[string]any
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	rec := captureRec{level: r.Level, msg: r.Message, kv: map[string]any{}}
	r.Attrs(func(a slog.Attr) bool {
		rec.kv[a.Key] = a.Value.Any()
		return true
	})
	h.mu.Lock()
	h.recs = append(h.recs, rec)
	h.mu.Unlock()
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

// find 返回消息含 sub 的第一条记录（logx 可能给消息加前缀，故用含判定）。
func (h *captureHandler) find(sub string) (captureRec, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.recs {
		if contains(r.msg, sub) {
			return r, true
		}
	}
	return captureRec{}, false
}

func (h *captureHandler) messages() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.recs))
	for i, r := range h.recs {
		out[i] = r.msg
	}
	return out
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// withShortHardExit 把 10s 兜底压到毫秒级（否则这条 Warn 要烧 10s 才验得到）。
func withShortHardExit(t *testing.T) {
	t.Helper()
	old := hardExitTimeout
	hardExitTimeout = 50 * time.Millisecond
	t.Cleanup(func() { hardExitTimeout = old })
}

// stuckPool 构造一个 Run 不理会 ctx 的 Pool，返回 started/blocked 两个闸：
// 等到 started 即证明任务已在跑；close(blocked) 才让它返回（避免 goroutine 泄漏）。
func stuckPool(t *testing.T, h *captureHandler) (p *Pool, started <-chan struct{}, unblock func()) {
	t.Helper()
	st := make(chan struct{})
	blocked := make(chan struct{})
	var once sync.Once
	p = &Pool{
		Queue: newMockQueue("stuck-task"),
		Log:   logx.NewSlogLogger("test", logx.WithHandler(h)),
		N:     1,
		Run: func(context.Context, string) error {
			once.Do(func() { close(st) })
			<-blocked // 刻意不 select ctx.Done()：模拟第三方 Run 不理会取消
			return nil
		},
	}
	return p, st, func() { close(blocked) }
}

// TestStopWarnsWhenWorkerIgnoresContext 硬取消后仍未退出必须留痕（#40）。
//
// 修复前的形态是**静默遗弃**：分支体为空，Stop 照样置 stopped 并打 Info
// 「stopped」，运维读到的是「干净停机」，而 goroutine 与任务都还在跑。
func TestStopWarnsWhenWorkerIgnoresContext(t *testing.T) {
	withShortHardExit(t)
	h := &captureHandler{}
	p, started, unblock := stuckPool(t, h)

	p.Start(context.Background())
	<-started
	p.Stop(20 * time.Millisecond) // grace 内排不空 ⇒ 走硬取消 ⇒ 兜底超时
	unblock()

	if rec, ok := h.find("stop_exit_timeout"); !ok {
		t.Fatalf("worker 未退出必须打 Warn 留痕，实际日志: %v", h.messages())
	} else {
		if rec.level != slog.LevelWarn {
			t.Errorf("该日志级别应为 Warn，实为 %v", rec.level)
		}
		// 「几个没退出」是这条日志唯一能给出的信息，必须是真数不是占位
		// slog 统一把整型归一到 int64
		if got := rec.kv["stuck_workers"]; got != int64(1) {
			t.Errorf("stuck_workers = %v (%T), want 1", got, got)
		}
		if got := rec.kv["workers"]; got != int64(1) {
			t.Errorf("workers = %v (%T), want 1", got, got)
		}
	}

	// 放开后 worker 才真正退出——证明「未退出」这个判定当时是准的
	deadline := time.Now().Add(5 * time.Second)
	for p.liveWorkers.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("超时：Run 返回后 worker 应退出，liveWorkers=%d", p.liveWorkers.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestStopCleanDrainDoesNotWarn 对照：正常排空不得打这条 Warn。
//
// 只有「开着绿」的用例等于没钉住——若 Warn 无条件打，前一条照样过。
func TestStopCleanDrainDoesNotWarn(t *testing.T) {
	withShortHardExit(t)
	h := &captureHandler{}
	p := &Pool{
		Queue: newMockQueue("fast-task"),
		Log:   logx.NewSlogLogger("test", logx.WithHandler(h)),
		N:     1,
		Run:   func(context.Context, string) error { return nil },
	}
	p.Start(context.Background())
	time.Sleep(50 * time.Millisecond) // 让任务被领走并跑完（唯一需要等的时序）
	p.Stop(2 * time.Second)

	if _, ok := h.find("stop_exit_timeout"); ok {
		t.Errorf("正常排空不得打 stop_exit_timeout，实际日志: %v", h.messages())
	}
	if _, ok := h.find("stop_heartbeat_timeout"); ok {
		t.Errorf("心跳应正常退出，实际日志: %v", h.messages())
	}
	if _, ok := h.find("stopped"); !ok {
		t.Errorf("正常停机应有 stopped Info，实际日志: %v", h.messages())
	}
}

// slowHeartbeatQueue 心跳调用阻塞（模拟一个卡在 DB/网络上的队列实现）。
type slowHeartbeatQueue struct {
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
	resetCnt atomic.Int32
}

func (q *slowHeartbeatQueue) ClaimNextPending(context.Context) (string, bool, error) {
	return "", false, nil
}

func (q *slowHeartbeatQueue) TouchRunningHeartbeats(context.Context) error {
	q.once.Do(func() { close(q.entered) })
	<-q.release
	return nil
}

func (q *slowHeartbeatQueue) ResetRunningToPending(context.Context, time.Duration) (int64, error) {
	q.resetCnt.Add(1)
	return 0, nil
}

// TestStopWarnsWhenHeartbeatRoundStuck 心跳轮次卡住时同样必须留痕。
//
// 这是 #40 同一函数里**同形**的另一处静默分支（hbDone 等待）。一起修的理由：
// 两处的失效形态逐字相同——Stop 返回、打 Info「stopped」、goroutine 还在跑。
func TestStopWarnsWhenHeartbeatRoundStuck(t *testing.T) {
	withShortHardExit(t)

	q := &slowHeartbeatQueue{entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(q.release) }) // 放开心跳轮次，不留 goroutine

	h := &captureHandler{}
	p := &Pool{
		Queue: q,
		Log:   logx.NewSlogLogger("test", logx.WithHandler(h)),
		N:     1,
		Run:   func(context.Context, string) error { return nil },
		// 心跳周期压到毫秒级（Pool 字段可配，queue#88）——否则首个 tick 要等 15s。
		HeartbeatInterval: 10 * time.Millisecond,
	}
	p.Start(context.Background())

	select {
	case <-q.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("超时：心跳轮次应已开始")
	}
	// 无在飞任务 ⇒ worker 很快退出 ⇒ 不进 grace_exceeded，直接走到停心跳
	p.Stop(2 * time.Second)

	rec, ok := h.find("stop_heartbeat_timeout")
	if !ok {
		t.Fatalf("心跳轮次卡住必须打 Warn 留痕，实际日志: %v", h.messages())
	}
	if rec.level != slog.LevelWarn {
		t.Errorf("该日志级别应为 Warn，实为 %v", rec.level)
	}
}
