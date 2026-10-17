// Package worker DB 即队列 worker pool。
// webhook/API 同步路径只落库（pending）即返回，N 个 worker 从表拉 pending（原子抢占）执行。
// 心跳续期 + 过期重置 + 两阶段优雅停机。
package worker

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/oekit/concurrency/async"
	"github.com/oekit/logx"
)

// 默认参数。
const (
	HeartbeatInterval = 20 * time.Second
	StaleRunningAfter = 90 * time.Second
	// queueCallTimeout 单次守护队列调用限时（心跳续期/过期复位共用）。
	queueCallTimeout = 5 * time.Second
)

// TaskQueue 任务队列存储接口（调用方实现，如 GORM/MySQL/Redis）。
type TaskQueue interface {
	// ClaimNextPending 原子抢占下一个 pending 任务（pending → running）。
	ClaimNextPending(ctx context.Context) (taskID string, ok bool, err error)
	// TouchRunningHeartbeats 续期本实例 running 任务的心跳。
	TouchRunningHeartbeats(ctx context.Context) error
	// ResetRunningToPending 重置心跳已死的 running 任务回 pending。
	ResetRunningToPending(ctx context.Context, staleAfter time.Duration) (int64, error)
}

// Pool worker 池。
type Pool struct {
	Queue TaskQueue
	// Run 单任务执行体。
	Run func(ctx context.Context, taskID string) error
	N   int
	Log logx.Logger
	// ClaimTimeout 单次 ClaimNextPending 的挂死兜底（0 = 5 分钟缺省）。
	// Queue 是调用方实现（GORM 等），与 runTask/claim 的 panic 隔离同一不变量：
	// 外部代码异常不得让 worker 永久僵死——超时按 claim 失败退避重试。
	ClaimTimeout time.Duration

	mu         sync.Mutex // 保护 start/stop 生命周期状态
	started    bool
	stopped    bool // Stop 置位：单次生命周期（二次 Start 与 Stop 后 Start 都 no-op）
	loopCtx    context.Context
	runCtx     context.Context
	hbCtx      context.Context // 心跳独立于 loopCtx：排空窗口内任务仍需续期
	cancelLoop context.CancelFunc
	cancelRun  context.CancelFunc
	cancelHB   context.CancelFunc
	done       chan struct{} // 全部 worker 退出后关闭
	hbDone     chan struct{} // 心跳 goroutine 退出后关闭
	stopOnce   sync.Once
}

func (p *Pool) logger() logx.Logger {
	if p.Log == nil {
		return logx.Delegating("agentkit-worker")
	}
	return p.Log
}

// Start 启动 N 个常驻 worker（重复调用为 no-op——字段不重置、旧 goroutine 不泄漏）。
func (p *Pool) Start(ctx context.Context) {
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		// 二次 Start 静默 no-op 是文档化契约——留观测面供误重启排查
		//（第九轮审计速赢：Stop 后 Start 同样落此分支，reason 区分形态）
		p.logger().Warn("agentkit.worker.start_ignored", "reason", "already_started")
		return
	}
	if p.stopped {
		p.mu.Unlock()
		// Stop 后 Start：单次生命周期契约（字段不重置、旧 goroutine 已收）——
		// 需要重启应构造新 Pool（工厂语义，v2 若开）
		p.logger().Warn("agentkit.worker.start_ignored", "reason", "stopped_pool_not_restartable")
		return
	}
	p.started = true
	if p.N <= 0 {
		p.N = 1
	}
	p.loopCtx, p.cancelLoop = context.WithCancel(ctx)
	p.runCtx, p.cancelRun = context.WithCancel(ctx)
	p.hbCtx, p.cancelHB = context.WithCancel(ctx)
	p.done = make(chan struct{})
	p.hbDone = make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < p.N; i++ {
		wg.Add(1)
		async.GoSafe(func() {
			defer wg.Done()
			p.loop(p.loopCtx, p.runCtx, i)
		})
	}
	async.GoSafe(func() {
		wg.Wait()
		close(p.done)
	})
	// 心跳独立等待：Stop 在任务排空后才停心跳（在途任务的心跳不能先死），
	// 故不与 worker 同 wg——done 关闭不依赖心跳退出
	async.GoWithContext(p.hbCtx, func(ctx context.Context) {
		defer close(p.hbDone)
		p.heartbeatLoop(ctx)
	})
	p.mu.Unlock()
	p.logger().Info("agentkit.worker.started", "workers", p.N)
}

func (p *Pool) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// 两个调用各自限时，互不挤占；并行跑——原先串行时 resetStale 慢
			// 会把心跳挤出周期，本实例任务被对端误判过期。
			// 心跳 goroutine panic = 本实例全部在途任务心跳停止 → 90s 后被
			// 对端复位重跑（静默双跑），故与 claim/run 同等隔离
			var wg sync.WaitGroup
			wg.Add(2)
			// 心跳探测 goroutine 与 claim/run 同等 panic 隔离（ekit async 收编
			// 裸 go 的治理纪律——探测死一个不能打死本实例全部心跳）
			async.GoSafe(func() { defer wg.Done(); p.heartbeat() })
			async.GoSafe(func() { defer wg.Done(); p.resetStale() })
			wg.Wait()
		}
	}
}

// heartbeat TouchRunningHeartbeats（panic 隔离 + 限时，骨架见 guardedCall）。
func (p *Pool) heartbeat() {
	_ = p.guardedCall("agentkit.worker.heartbeat_panic", "agentkit.worker.heartbeat_failed",
		p.Queue.TouchRunningHeartbeats)
}

// resetStale ResetRunningToPending（panic 隔离 + 限时，骨架见 guardedCall）。
func (p *Pool) resetStale() {
	var n int64
	_ = p.guardedCall("agentkit.worker.stale_reset_panic", "agentkit.worker.stale_reset_failed",
		func(ctx context.Context) error {
			var err error
			n, err = p.Queue.ResetRunningToPending(ctx, StaleRunningAfter)
			return err
		})
	if n > 0 {
		p.logger().Warn("agentkit.worker.stale_running_reset", "count", n,
			"stale_after", StaleRunningAfter.String())
	}
}

// guardedCall 守护队列调用的统一骨架：panic 隔离（记 panicLog）+ queueCallTimeout
// 独立限时 ctx + 失败 Warn（failLog）。Queue 是调用方实现（GORM 等），panic 若穿透
// 会杀死心跳 goroutine——本实例全部在途任务心跳停止 → 90s 后被对端复位重跑
// （静默双跑），故与 claim/runTask 同等隔离。
func (p *Pool) guardedCall(panicLog, failLog string, fn func(ctx context.Context) error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			p.logger().Error(panicLog, "panic", fmt.Sprint(r))
			err = fmt.Errorf("worker: queue panic: %v", r)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), queueCallTimeout)
	defer cancel()
	if err = fn(ctx); err != nil {
		p.logger().Warn(failLog, "error", err.Error())
	}
	return err
}

// Stop 两阶段停机：取消领取循环 → 等 grace（排空期间心跳继续续期）→ 硬取消执行 →
// 停心跳。幂等。未启动时调用为 no-op（不消耗停机状态——Stop 在 Start 之前
// 误调用后，真正的 Stop 依然有效）。
func (p *Pool) Stop(grace time.Duration) {
	p.mu.Lock()
	if !p.started {
		p.mu.Unlock()
		return
	}
	cancelLoop, cancelRun, cancelHB, done, hbDone, log :=
		p.cancelLoop, p.cancelRun, p.cancelHB, p.done, p.hbDone, p.logger()
	p.mu.Unlock()

	p.stopOnce.Do(func() {
		cancelLoop()
		select {
		case <-done:
		case <-time.After(grace):
			log.Warn("agentkit.worker.stop_grace_exceeded",
				"grace", grace.String(), "action", "硬取消在途任务")
			cancelRun()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
			}
		}
		// 心跳最后停：排空窗口内（含硬取消后的收尾）在途任务仍需续期，
		// 提前停会让对端按 StaleRunningAfter 复位仍在执行的任务造成双跑
		cancelHB()
		select {
		case <-hbDone:
		case <-time.After(10 * time.Second): // 心跳调用各自限时 5s，兜底不悬挂
		}
		p.mu.Lock()
		p.stopped = true
		p.mu.Unlock()
		log.Info("agentkit.worker.stopped")
	})
}

func (p *Pool) loop(loopCtx, runCtx context.Context, id int) {
	for {
		select {
		case <-loopCtx.Done():
			return
		default:
		}
		taskID, ok, err := p.claim(loopCtx)
		if err != nil {
			if loopCtx.Err() != nil {
				return
			}
			p.logger().Error("agentkit.worker.claim_failed", "worker", id, "error", err.Error())
			sleepCtx(loopCtx, time.Second)
			continue
		}
		if !ok {
			sleepCtx(loopCtx, 500*time.Millisecond)
			continue
		}
		p.logger().Info("agentkit.worker.claimed", "worker", id, "task", taskID)
		p.runTask(runCtx, id, taskID)
	}
}

// claim ClaimNextPending 的 panic 隔离包装：Queue 是调用方实现（GORM 等），
// panic 若穿透会杀死该 worker goroutine——池静默减员（与 runTask 隔离同一不变量）。
// 恢复后按 claim 失败路径退避重试。挂死经 ClaimTimeout 兜底。
func (p *Pool) claim(ctx context.Context) (taskID string, ok bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("worker: queue panic: %v", r)
		}
	}()
	timeout := p.ClaimTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return p.Queue.ClaimNextPending(cctx)
}

// runTask 单任务执行（panic 隔离）：任务 panic 只损失该任务——worker 存活继续
// 拉取，任务由 StaleRunningAfter 心跳过期机制复位重跑；不隔离则 panic 杀死
// 整个 loop goroutine，池静默减员。
func (p *Pool) runTask(runCtx context.Context, id int, taskID string) {
	defer func() {
		if r := recover(); r != nil {
			p.logger().Error("agentkit.worker.task_panic", "worker", id, "task", taskID,
				"panic", fmt.Sprint(r))
		}
	}()
	if err := p.Run(runCtx, taskID); err != nil {
		p.logger().Error("agentkit.worker.run_error", "worker", id, "task", taskID, "error", err.Error())
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
