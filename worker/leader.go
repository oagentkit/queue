// worker 包选主原语：多副本部署时"只能跑一份"的组件（出站轮询、定时清理、
// 通知汇总等）经租约竞争，与 DB 即队列的 worker 池组成完整的横向扩展故事——
// 任务面靠 ClaimNextPending 原子抢占天然分片，控制面靠租约选主收敛为单份。
//
// 选主语义：任期 = ttl；持有者失联（进程崩溃）后 ≤ttl 自动换主；
// 优雅停机主动让位（Release）；脑裂窗口 ≤ 一个轮询间隔，调用方组件需容忍
// 短暂双主（Argus 轮询任务源幂等双查，双跑无害只浪费）。
package worker

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oekit/concurrency/async"
	"github.com/oekit/logx"
)

// LeaseStore 租约存储接口（调用方按存储实现：SQL 一行条件 UPSERT 即可）。
// 语义必须原子：TryAcquire 的"读-判-写"不允许并发穿插（SQL 用
// `UPDATE ... WHERE key=? AND (holder=? OR expires_at<?)` 条件更新实现）。
type LeaseStore interface {
	// TryAcquire 原子获取或续约租约。key 空闲/已过期/持有者就是 holder →
	// 置 holder 并顺延 ttl，返回 true；他人持有未过期 → 返回 false。
	TryAcquire(ctx context.Context, key, holder string, ttl time.Duration) (bool, error)
	// Release 释放租约（仅持有者本人生效；优雅停机让位）。
	// 非持有者或 key 不存在 → no-op 返回 nil。
	Release(ctx context.Context, key, holder string) error
}

// LeaderElector 租约选主器：周期 TryAcquire 竞争，IsLeader 反映最近一次结果，
// 角色翻转时触发 onGained/onLost 回调。
type LeaderElector struct {
	store    LeaseStore
	key      string
	holder   string
	ttl      time.Duration
	interval time.Duration
	onGained func()
	onLost   func()
	log      logx.Logger

	mu        sync.Mutex
	leader    bool
	stop      chan struct{}
	done      chan struct{} // 竞选 goroutine 退出后关闭（Stop 等待让位完成）
	started   atomic.Bool
	stopOnce  sync.Once
	startOnce sync.Once
}

// LeaderOption 选主器可选项。
type LeaderOption func(*LeaderElector)

// WithOnGained 获得领导权回调（首次当选与失而复得均触发）。
func WithOnGained(f func()) LeaderOption { return func(e *LeaderElector) { e.onGained = f } }

// WithOnLost 失去领导权回调（续约失败；Stop 主动让位不触发——那是计划内交接）。
func WithOnLost(f func()) LeaderOption { return func(e *LeaderElector) { e.onLost = f } }

// WithLeaderLogger 日志（nil 安全，缺省静默）。
func WithLeaderLogger(log logx.Logger) LeaderOption {
	return func(e *LeaderElector) {
		if log != nil {
			e.log = log
		}
	}
}

// NewLeaderElector 创建选主器。interval 建议 ttl/3（失主换主延迟 ≈ ttl + interval）。
func NewLeaderElector(store LeaseStore, key, holder string, ttl, interval time.Duration,
	opts ...LeaderOption) *LeaderElector {
	e := &LeaderElector{
		store:    store,
		key:      key,
		holder:   holder,
		ttl:      ttl,
		interval: interval,
		log:      logx.Delegating("agentkit-leader"),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Start 启动竞选循环：立即竞争一次（缩短冷启动等待），此后按 interval 续约。
// ctx 取消或 Stop() 退出；退出前若持有租约则主动让位（计划内交接，不触发 onLost）。
// 重复调用为 no-op（只允许一个竞选 goroutine）。
func (e *LeaderElector) Start(ctx context.Context) {
	e.startOnce.Do(func() {
		// started 同步置位（在拉起 goroutine 之前）：Stop 若在 goroutine 尚未
		// 跑到 Store(true) 时读到 false 会直接返回，既不等待也不让位，租约泄漏。
		e.started.Store(true)
		// GoWithContext（ekit v0.29 补齐的携带 ctx 安全 goroutine）：tick 与
		// select 消费同一个显式传入的 ctx
		async.GoWithContext(ctx, func(ctx context.Context) {
			defer close(e.done)
			defer e.started.Store(false)
			ticker := time.NewTicker(e.interval)
			defer ticker.Stop()
			if !e.tick(ctx) {
				e.yield()
				return
			}
			for {
				select {
				case <-ctx.Done():
					e.yield()
					return
				case <-e.stop:
					e.yield()
					return
				case <-ticker.C:
					if !e.tick(ctx) {
						e.yield()
						return
					}
				}
			}
		})
	})
}

// IsLeader 当前是否持有领导权（最近一次续约结果）。
func (e *LeaderElector) IsLeader() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.leader
}

// Stop 停止竞选并主动让位（幂等；优雅停机的计划内交接，不触发 onLost）。
// 等待竞选 goroutine 退出后返回——返回后 IsLeader 必为 false 且租约已让出。
// Start 未被调用时立即返回（无租约可让）。
func (e *LeaderElector) Stop() {
	e.stopOnce.Do(func() { close(e.stop) })
	if e.started.Load() {
		<-e.done
	}
}

// stopping 已请求停止。
func (e *LeaderElector) stopping() bool {
	select {
	case <-e.stop:
		return true
	default:
		return false
	}
}

// releaseTimeout 让位释放租约的限时（独立 Background ctx：原 ctx 往往已取消）。
const releaseTimeout = 5 * time.Second

// acquireTimeout 竞选 TryAcquire 的单次限时（存储挂起时 Stop 不至于无界）。
const acquireTimeout = 5 * time.Second

// releaseLease 主动释放租约（计划内交接）：失败仅 Warn——租约 ttl 到期会自然失效，
// 让位失败不阻断停机流程。
func (e *LeaderElector) releaseLease() {
	ctx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
	defer cancel()
	if err := e.store.Release(ctx, e.key, e.holder); err != nil {
		e.log.Warn("agentkit.leader.release_failed", "key", e.key, "error", err.Error())
	}
}

// tick 竞争一次租约。返回 false = 竞选应终止（已停止或 ctx 取消）。
func (e *LeaderElector) tick(ctx context.Context) bool {
	// 停止/取消后不再发起新的竞争：避免"Stop 之后新获得租约"的让位遗漏
	if e.stopping() || ctx.Err() != nil {
		return false
	}
	// 独立限时（继承 caller ctx 保留父取消语义）：存储层挂起（连接池耗尽/
	// 网络分区）时 Stop 不至于永久阻塞在 <-e.done——与 releaseTimeout 同口径
	acquireCtx, cancel := context.WithTimeout(ctx, acquireTimeout)
	defer cancel()
	ok, err := e.store.TryAcquire(acquireCtx, e.key, e.holder, e.ttl)
	if err != nil {
		if ctx.Err() != nil {
			return false // ctx 取消导致的失败不算失主，退出走 yield 让位
		}
		if errors.Is(err, context.DeadlineExceeded) {
			// 本限时触发（父 ctx 未取消）：按存储抖动同路——保守失主、下周期重试
			e.setLeader(false)
			e.log.Warn("agentkit.leader.acquire_timeout", "key", e.key)
			return true
		}
		// 存储抖动：保守认为失主（回调方组件按非 leader 收敛），下个周期重试
		e.setLeader(false)
		e.log.Warn("agentkit.leader.acquire_failed", "key", e.key, "error", err.Error())
		return true
	}
	if ok && e.stopping() {
		// Stop 与 TryAcquire 穿插：刚获得的租约立即让位，不宣布当选
		e.releaseLease()
		return false
	}
	e.setLeader(ok)
	return true
}

// yield 退出时让位：若持有租约则释放（计划内交接，不触发 onLost）。
func (e *LeaderElector) yield() {
	e.mu.Lock()
	wasLeader := e.leader
	e.leader = false
	e.mu.Unlock()
	if !wasLeader {
		return
	}
	e.releaseLease()
	e.log.Info("agentkit.leader.yielded", "key", e.key, "holder", e.holder)
}

func (e *LeaderElector) setLeader(v bool) {
	e.mu.Lock()
	if e.stopping() {
		// 已停机：yield 负责让位，此处不再变更状态/触发回调
		// （避免 Stop 之后才触发 onGained，或让位后误触发 onLost）
		e.leader = false
		e.mu.Unlock()
		return
	}
	prev := e.leader
	e.leader = v
	e.mu.Unlock()
	if v && !prev {
		e.log.Info("agentkit.leader.gained", "key", e.key, "holder", e.holder, "ttl", e.ttl.String())
		if e.onGained != nil {
			e.onGained()
		}
	} else if !v && prev {
		e.log.Warn("agentkit.leader.lost", "key", e.key, "holder", e.holder)
		if e.onLost != nil {
			e.onLost()
		}
	}
}
