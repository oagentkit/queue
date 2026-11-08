// Package worker DB 即队列 worker pool。
// webhook/API 同步路径只落库（pending）即返回，N 个 worker 从表拉 pending（原子抢占）执行。
// 心跳续期 + 过期重置 + 两阶段优雅停机。
package worker

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oekit/concurrency/async"
	"github.com/oekit/concurrency/loop"
	"github.com/oekit/logx"
)

// 默认参数。
const (
	HeartbeatInterval = 20 * time.Second
	StaleRunningAfter = 90 * time.Second
	// queueCallTimeout 单次守护队列调用限时（心跳续期/过期复位共用）。
	queueCallTimeout = 5 * time.Second
)

// heartbeatInterval 心跳周期（缺省等于导出的 HeartbeatInterval）。用 var 的
// 唯一理由同 hardExitTimeout：用例要把 20s 压到毫秒级，才能让心跳轮次在
// Stop 时真的「在飞」——否则那条兜底 Warn 永远验不到。
var heartbeatInterval = HeartbeatInterval

// hardExitTimeout 停机第二段/心跳等待的兜底：超过即认定对方不理会 ctx，
// 打 Warn 留痕后继续收尾（不能因为一个卡死的 goroutine 让 Stop 永不返回）。
//
// 用 var 不用 const：用例要把 10s 压到毫秒级才能验到那条 Warn，否则单个
// 用例固定烧 10s。除测试外无人改它。
var hardExitTimeout = 10 * time.Second

// TaskQueue 任务队列存储接口（调用方实现，如 GORM/MySQL/Redis）。
//
// # 时序约束（宿主自实现者的正确性红线，oagentkit/queue#25）
//
// TouchRunningHeartbeats 与 ResetRunningToPending 由 Pool 两个独立 goroutine
// 分别调用（周期由宿主经 Pool 字段配置），两者的组合约束没有兜底——配错
// 不报错，只以「同一任务双跑」的形式爆出来：
//
//   - 心跳周期 T_heartbeat 必须 ≪ staleAfter（ResetRunningToPending 的失联
//     阈值）：**量化建议 ≥5×**（argus 在役配比 stale_after=90s / 心跳 10s）。
//     反例：心跳 60s、staleAfter 90s——一次 GC 停顿或网络抖动错过一个心跳
//     周期，running 任务即被误判失联重置 → 另一副本接手 → 双跑；
//   - ResetRunningToPending 的 staleAfter 必须 > 单任务的正常执行时长上限：
//     任务执行超过 staleAfter 且心跳同步失败时，重置会在任务仍在跑时发生
//     （心跳在跑 = 存活信号，实现方不得只在任务结束时写）；
//   - 优雅停机（Pool.Stop 的 grace 排水）窗口内**心跳继续续期**：Pool 用
//     独立于 loopCtx 的 hbCtx 驱动心跳 goroutine（排水中任务仍活着，不该
//     被他副本判失联接管——自实现的排水逻辑要保住同一承诺）；
//   - ClaimNextPending 的状态迁移必须单语句原子（WHERE status='pending'
//     守卫），SELECT-then-UPDATE 两步会竞窗双抢。
//
// 参考实现：sqlqueue.Store（database/sql，PostgreSQL/MySQL/SQLite 三方言，
// 上述纪律一次做对）；argus 的 DB 即队列实现（三方言在役）是素材来源。
type TaskQueue interface {
	// ClaimNextPending 原子抢占下一个 pending 任务（pending → running）。
	ClaimNextPending(ctx context.Context) (taskID string, ok bool, err error)
	// TouchRunningHeartbeats 续期本实例 running 任务的心跳。
	//
	// 实现方若想支持「本进程已不再执行的任务尽快失联复位」，请**额外**实现
	// ScopedHeartbeater：Pool 会优先走按在飞任务集续期，本方法不再被调用。
	// 未实现时本方法是盲续——它按 runner_id 续期全部 running 行，包括
	// runTask 已 panic/报错退出、但行仍停在 running 的任务（oagentkit/queue#42）。
	TouchRunningHeartbeats(ctx context.Context) error
	// ResetRunningToPending 重置心跳已死的 running 任务回 pending。
	ResetRunningToPending(ctx context.Context, staleAfter time.Duration) (int64, error)
}

// ScopedHeartbeater 是 TaskQueue 的**可选能力**（类型断言探测，不改 TaskQueue
// 接口本身）：TaskQueue 的实现方在仓外（宿主自实现的 GORM/Redis 队列），
// 往接口里加方法就是破坏性变更——所有现有实现当场编译不过。用可选接口
// 表达「能做得更好的一种实现」，未实现者行为一字不变。
//
// 实现本接口后，Pool 的心跳只续期「本进程真正在跑」的任务；runTask 返回
// （成功、报错、panic）后该任务立即移出续期集，其 heartbeat_at 随之冻结，
// staleAfter 后由 ResetRunningToPending 复位重跑——这正是
// Pool.runTask 注释宣称的恢复契约。
//
// # 为什么必须按任务集续期（oagentkit/queue#42）
//
// 盲续（TouchRunningHeartbeats）的结构性缺陷不是一个 bug 而是一个
// 不可能同时成立的两难：
//
//   - 任务 panic/报错后，行仍停在 status='running' + runner_id=本实例
//     （Pool 不写终态，宿主负责）；
//   - 只要本进程还活着（继续领别的任务、心跳继续跑），盲续就会把这些行
//     的心跳一直续成新鲜的；
//   - 于是 ResetRunningToPending(staleAfter) 永不命中——「心跳过期复位」
//     这条恢复路径对「同进程内失败的任务」**结构性失效**，任务永久卡
//     running 且无人标记终态（静默丢失）。
//
// 排除集（只续期在飞任务）把两件事分开了：「我在跑它」是活信号，
// 「它归我」不是。
//
// # 实现方的三条约束
//
//   - 只动 caller 传入的 taskIDs，且必须同时按 runner_id 与 status='running'
//     守卫（不得续期他实例的行，也不得把已终态的行续回新鲜）；
//   - 传入空集时**不要**当成「续期全部」——Pool 在空集时根本不会调用，
//     但实现方自守一层可以防住「空切片 = 无过滤条件」这类 SQL 拼装错误；
//   - 集大小受 Pool.N 约束（同时最多 N 个在飞任务），无需分页。
type ScopedHeartbeater interface {
	// TouchHeartbeatsFor 只续期 taskIDs 中属于本实例且在跑的任务心跳。
	TouchHeartbeatsFor(ctx context.Context, taskIDs []string) error
}

// DetailedClaimer 是 TaskQueue 的**可选加强**（类型断言探测，不改 TaskQueue
// 接口本身——实现方在仓外，往接口里加方法是破坏性变更，与 ScopedHeartbeater
// 同一道兼容性纪律，oagentkit/queue#70）：把领取时点的 attempts（重试计数）
// 透出到消费侧。TaskQueue 的三方法面拿不到它，宿主要实现「毒任务闸门」
// （重试超 N 次改走本地死信/告警）就只能直查队列内部表——接口恰好把
// attempts 藏了起来，这正是审计 issue 指出的观测面缺口。
//
// Pool 在 Queue 实现本接口时改走 detailed claim，attempts 随 claimed 日志
// 落盘（毒任务的重复领取在运维侧可见）；未实现时走原三方法路径，行为不变。
//
// 参考实现：sqlqueue.Store.ClaimNextPendingDetailed（attempts 含本次领取：
// 首次领取 = 1，失联复位一轮后重领 = 3）。重试上限与死信终态见 sqlqueue 的
// WithMaxAttempts / StatusDeadLetter。
type DetailedClaimer interface {
	// ClaimNextPendingDetailed 原子抢占下一个 pending 任务并返回其 attempts。
	// 语义与 ClaimNextPending 完全一致（同一原子守卫），仅多透出计数；
	// ok=false 时 attempts 无意义（实现方应返回 0）。
	ClaimNextPendingDetailed(ctx context.Context) (taskID string, attempts int, ok bool, err error)
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

	inflightMu sync.Mutex          // 保护 inflight
	inflight   map[string]struct{} // 本进程正在执行的任务（心跳续期集，见 ScopedHeartbeater）
	// liveWorkers 尚未退出的 worker goroutine 数（atomic）。只在停机超时
	// 分支里被读——「还有几个没退出」是那条 Warn 唯一能给出的硬信息。
	liveWorkers int32

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
		return logx.Delegating("oagentkit-worker")
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
	p.inflightMu.Lock()
	p.inflight = make(map[string]struct{}, p.N)
	p.inflightMu.Unlock()
	atomic.StoreInt32(&p.liveWorkers, int32(p.N))
	var wg sync.WaitGroup
	for i := 0; i < p.N; i++ {
		wg.Add(1)
		// worker 循环体 panic = 该 worker 死亡、池静默减员——panic 汇报收编
		// 进组件 logger（与 claim_failed/task_panic 同一观测面；旧 async.GoSafe
		// 走全局兜底，组件视角不可见）
		p.panicRunner("agentkit.worker.loop_panic").GoSafe(func() {
			defer wg.Done()
			defer atomic.AddInt32(&p.liveWorkers, -1)
			p.loop(p.loopCtx, p.runCtx, i)
		})
	}
	async.GoSafe(func() {
		wg.Wait()
		close(p.done)
	})
	// 心跳循环收编 concurrency/loop（ctx 感知 + 忙时丢 tick + 单轮 panic 收编
	// + 停机排水）；FirstDelay 对齐旧 ticker 节拍（首跑在首个 interval 后）。
	// 心跳独立等待：Stop 在任务排空后才停心跳（在途任务的心跳不能先死），
	// 故不与 worker 同 wg——done 关闭不依赖心跳退出
	hbLoop := loop.Run(p.hbCtx, loop.Config{
		Interval:   heartbeatInterval,
		FirstDelay: heartbeatInterval,
		Name:       "agentkit.worker.heartbeat",
	}, func(ctx context.Context) {
		// 两个调用各自限时，互不挤占；并行跑——原先串行时 resetStale 慢
		// 会把心跳挤出周期，本实例任务被对端误判过期。探测 goroutine 与
		// claim/run 同等 panic 隔离（探测死一个不能打死本实例全部心跳）
		var probeWg sync.WaitGroup
		probeWg.Add(2)
		async.GoSafe(func() { defer probeWg.Done(); p.heartbeat() })
		async.GoSafe(func() { defer probeWg.Done(); p.resetStale() })
		probeWg.Wait()
	})
	async.GoWithContext(p.hbCtx, func(ctx context.Context) {
		defer close(p.hbDone)
		hbLoop.Wait() // 停机排水：在飞轮次跑完
	})
	p.mu.Unlock()
	p.logger().Info("agentkit.worker.started", "workers", p.N)
}

// heartbeat 续期本实例心跳（panic 隔离 + 限时，骨架见 guardedCall）。
//
// Queue 实现 ScopedHeartbeater 时按本进程在飞任务集续期（空集直接跳过，
// 不发无过滤条件的 UPDATE）；否则退回盲续 TouchRunningHeartbeats——
// 那条路径下「panic 任务靠心跳过期复位」不成立，见 ScopedHeartbeater 文档。
func (p *Pool) heartbeat() {
	sh, scoped := p.Queue.(ScopedHeartbeater)
	if !scoped {
		_ = p.guardedCall("agentkit.worker.heartbeat_panic", "agentkit.worker.heartbeat_failed",
			p.Queue.TouchRunningHeartbeats)
		return
	}
	ids := p.inflightSnapshot()
	if len(ids) == 0 {
		// 无在飞任务：不调用。盲续在这里恰好是「把已失败任务续活」的那一步，
		// 所以「没事可续」必须是**不发语句**而不是「发一条全量续期」。
		return
	}
	_ = p.guardedCall("agentkit.worker.heartbeat_panic", "agentkit.worker.heartbeat_failed",
		func(ctx context.Context) error { return sh.TouchHeartbeatsFor(ctx, ids) })
}

// trackStart 把任务登记进续期集（claim 成功后、Run 之前调用）。
func (p *Pool) trackStart(taskID string) {
	p.inflightMu.Lock()
	if p.inflight == nil { // 未经 Start 直接驱动 loop 的用法（测试/自实现）兜底
		p.inflight = make(map[string]struct{})
	}
	p.inflight[taskID] = struct{}{}
	p.inflightMu.Unlock()
}

// trackDone 把任务移出续期集（runTask 返回后无条件调用——成功/报错/panic
// 三种收尾都要移出，否则恢复契约对它不成立）。
func (p *Pool) trackDone(taskID string) {
	p.inflightMu.Lock()
	delete(p.inflight, taskID)
	p.inflightMu.Unlock()
}

// inflightSnapshot 取续期集的稳定快照（排序：日志与 SQL 参数顺序可复现）。
func (p *Pool) inflightSnapshot() []string {
	p.inflightMu.Lock()
	ids := make([]string, 0, len(p.inflight))
	for id := range p.inflight {
		ids = append(ids, id)
	}
	p.inflightMu.Unlock()
	sort.Strings(ids)
	return ids
}

// runTracked 登记 → 执行 → 注销。defer 保证 panic 路径也注销（runTask 已
// 自隔离 panic，这里是不依赖其实现的兜底）。
func (p *Pool) runTracked(runCtx context.Context, id int, taskID string) {
	p.trackStart(taskID)
	defer p.trackDone(taskID)
	p.runTask(runCtx, id, taskID)
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

// noopPanicReport panic 汇报职责在调用方路径的站点用（claim：loop 的
// claim_failed 日志已含 worker id 与错误文案）——Handler 置空抑制全局
// PanicHandler 的 stderr 兜底，库形态消费方不接 SetPanicHandler 的纪律延续。
var noopPanicReport async.PanicHandler = func(context.Context, any, []byte) {}

// panicRunner 站点专属 panic 收编（async.Runner，concurrency#12/#13 落地后
// 回收手搓 recover）：Handler 把 panic 值与栈路由到组件 logger 的站点专属
// 消息键——v0.15.4 裁定的结构化观测面得以保留，且手搓 recover 时代丢失的
// panic 栈现在随日志落盘。Runner 是值类型（一个 Handler 字段），按调用点
// 构造零成本；panic 同时收编为 *async.PanicError 返回，errors.As 可取。
func (p *Pool) panicRunner(panicLog string) async.Runner {
	return async.Runner{Handler: func(ctx context.Context, r any, stack []byte) {
		p.logger().Error(panicLog, "panic", r, "stack", string(stack))
	}}
}

// guardedCall 守护队列调用的统一骨架：panic 隔离（记 panicLog，含栈）+
// queueCallTimeout 独立限时 ctx + 失败 Warn（failLog）。Queue 是调用方实现（GORM 等），
// panic 若穿透会杀死心跳 goroutine——本实例全部在途任务心跳停止 → 90s 后被
// 对端复位重跑（静默双跑），故与 claim/runTask 同等隔离。
func (p *Pool) guardedCall(panicLog, failLog string, fn func(ctx context.Context) error) error {
	err := p.panicRunner(panicLog).RunSafeErr(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), queueCallTimeout)
		defer cancel()
		return fn(ctx)
	})
	var pe *async.PanicError
	if errors.As(err, &pe) {
		// 文案与手搓 recover 版逐字一致（panic 值入文案；栈已随 Handler 落日志）
		return fmt.Errorf("worker: queue panic: %v", pe.Value)
	}
	if err != nil {
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
	cancelLoop, cancelRun, cancelHB, done, hbDone, log, workers :=
		p.cancelLoop, p.cancelRun, p.cancelHB, p.done, p.hbDone, p.logger(), p.N
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
			case <-time.After(hardExitTimeout):
				// 静默遗弃是这里最坏的选项：worker 卡在不理会 ctx 的 Run 里，
				// Stop 却照样置 stopped 并打 Info，运维读到的是「干净停机」。
				// 状态位仍置（Pool 单次生命周期，stopOnce 已消耗——泄漏的
				// goroutine 没有救回来的路径），所以留痕是唯一诚实的做法。
				log.Warn("agentkit.worker.stop_exit_timeout",
					"stuck_workers", atomic.LoadInt32(&p.liveWorkers),
					"workers", workers,
					"waited", hardExitTimeout.String(),
					"action", "worker 未在硬取消后退出（Run 不理会 ctx？），本 Pool 已标记 stopped，这些 goroutine 仍在运行")
			}
		}
		// 心跳最后停：排空窗口内（含硬取消后的收尾）在途任务仍需续期，
		// 提前停会让对端按 StaleRunningAfter 复位仍在执行的任务造成双跑
		cancelHB()
		select {
		case <-hbDone:
		case <-time.After(hardExitTimeout): // 心跳调用各自限时 5s，兜底不悬挂
			log.Warn("agentkit.worker.stop_heartbeat_timeout",
				"waited", hardExitTimeout.String(),
				"action", "心跳循环未退出，其 goroutine 仍在运行")
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
		taskID, attempts, ok, err := p.claim(loopCtx)
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
		// attempts 仅在 Queue 实现 DetailedClaimer 时 > 0（queue#70）：
		// 毒任务的重复领取凭这个字段在 claimed 日志里现形
		kv := []any{"worker", id, "task", taskID}
		if attempts > 0 {
			kv = append(kv, "attempts", attempts)
		}
		p.logger().Info("agentkit.worker.claimed", kv...)
		p.runTracked(runCtx, id, taskID)
	}
}

// claim ClaimNextPending 的 panic 隔离包装（async.Runner 收编手搓 recover）：
// Queue 是调用方实现（GORM 等），panic 若穿透会杀死该 worker goroutine——池
// 静默减员（与 runTask 隔离同一不变量）。恢复后按 claim 失败路径退避重试。
// 挂死经 ClaimTimeout 兜底。
//
// Queue 实现 DetailedClaimer 时改走 detailed claim（queue#70），attempts 随
// 返回值透出（普通实现 attempts=0，行为不变）。
func (p *Pool) claim(ctx context.Context) (taskID string, attempts int, ok bool, err error) {
	err = async.Runner{Handler: noopPanicReport}.RunSafeErr(func() error {
		timeout := p.ClaimTimeout
		if timeout <= 0 {
			timeout = 5 * time.Minute
		}
		cctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if dc, detailed := p.Queue.(DetailedClaimer); detailed {
			taskID, attempts, ok, err = dc.ClaimNextPendingDetailed(cctx)
		} else {
			taskID, ok, err = p.Queue.ClaimNextPending(cctx)
		}
		return err
	})
	var pe *async.PanicError
	if errors.As(err, &pe) {
		err = fmt.Errorf("worker: queue panic: %v", pe.Value)
	}
	return taskID, attempts, ok, err
}

// runTask 单任务执行（panic 隔离，async.Runner 收编手搓 recover）：任务 panic
// 只损失该任务——worker 存活继续拉取；不隔离则 panic 杀死整个 loop goroutine，
// 池静默减员。
//
// 恢复契约（「任务由 StaleRunningAfter 心跳过期机制复位重跑」）**只在 Queue
// 实现 ScopedHeartbeater 时成立**：盲续实现会把这个已退出任务的
// heartbeat_at 一直续成新鲜的，reset 永不命中（oagentkit/queue#42）。
// 调用点在 runTracked 里，它负责把任务移出续期集。
func (p *Pool) runTask(runCtx context.Context, id int, taskID string) {
	err := async.Runner{Handler: func(ctx context.Context, r any, stack []byte) {
		p.logger().Error("agentkit.worker.task_panic", "worker", id, "task", taskID,
			"panic", r, "stack", string(stack))
	}}.RunSafeErr(func() error { return p.Run(runCtx, taskID) })
	if err != nil {
		var pe *async.PanicError
		if !errors.As(err, &pe) { // panic 已随 Handler 落 task_panic 日志
			p.logger().Error("agentkit.worker.run_error", "worker", id, "task", taskID, "error", err.Error())
		}
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
