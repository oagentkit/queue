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
	"fmt"
	"sync"
	"time"

	"github.com/oekit/concurrency/async"
	"github.com/oekit/logx"
)

// LeaseStore 租约存储接口（调用方按存储实现：SQL 一行条件 UPSERT 即可）。
// 语义必须原子：TryAcquire 的"读-判-写"不允许并发穿插（SQL 用
// `UPDATE ... WHERE key=? AND (holder=? OR expires_at<?)` 条件更新实现）。
//
// ⚠️ 时钟语义（自实现者的正确性红线，oagentkit/queue#26）：expires 的写入与
// 过期判定必须走 **DB 时钟**（同一时钟，如 `expires_at < NOW()` 配
// `expires_at = NOW() + ttl`），不得用应用侧 time.Now() 写入、DB 侧判定——
// 副本间应用时钟漂移会等量放大/缩短租约实际时长，削弱「失联后 ≤ttl 换主」
// 的承诺（NTP 正常时一切完美，漂移几秒时双头/换主延迟且无任何报错——隐蔽
// 失效形态，pglease v0.10.29 时代的审计修复，下游按旧实现复制的 LeaseStore
// 会静默丢掉它）。
//
// 已有参考实现，**默认路径是直接用**，不要按旧形态自写：
//   - PostgreSQL：pglease.PGLeaseStore（WithTable 换表名；本包 re-export 为
//     NewPGLeaseStore / PGLeaseStore 别名）
//   - MySQL / SQLite / PostgreSQL：sqllease.Store（database/sql 三方言）
//
// 有自定义需求（非 SQL 存储、多 key 批量等）再自实现——接口不变。
//
// ⚠️ 实现方**必须**对空 key / 空 holder 报错（而不是当成普通值）：
// 「本人持有则顺延」的判定是内容比对（`holder = ?` / `holder = EXCLUDED.holder`），
// 空串对它恒真 ⇒ 所有空 holder 副本互相认作本人 ⇒ 双主且零报错（queue#43）。
// NewLeaderElector 的构造期 panic 只拦按规矩走它的调用方，直接用本接口的
// 调用方需要库层这道护栏。
type LeaseStore interface {
	// TryAcquire 原子获取或续约租约。key 空闲/已过期/持有者就是 holder →
	// 置 holder 并顺延 ttl，返回 true；他人持有未过期 → 返回 false。
	// key 或 holder 为空 → 返回错误（不得静默当成普通值，见上方 ⚠️）。
	TryAcquire(ctx context.Context, key, holder string, ttl time.Duration) (bool, error)
	// Release 释放租约（仅持有者本人生效；优雅停机让位）。
	// 非持有者或 key 不存在 → no-op 返回 nil。
	// key 或 holder 为空 → 返回错误（空 holder 的 DELETE 会删掉
	// `holder = ''` 那把锁——正是它认不出的那把）。
	Release(ctx context.Context, key, holder string) error
}

// TokenLeaseStore 是 LeaseStore 的**可选**加强：把「怎么容忍脑裂窗口」从
// 调用方的玄学变成库层选项（queue#39）。
//
// 为什么是可选的第二个接口而不是给 LeaseStore 加方法：LeaseStore 是公开契约，
// 往里加方法会**编译期打断**所有既有实现与下游复刻（包括本仓 sqllease.Store
// 的三方言实现）。可选接口让「需要 fencing 的调用方」用类型断言取能力，
// 「不需要的」一行代码都不动：
//
//	if tls, ok := store.(worker.TokenLeaseStore); ok {
//		ok, gen, err := tls.TryAcquireWithToken(ctx, key, holder, ttl)
//	}
//
// 两档容忍粒度的差别（详见 pglease 包文档）：
//   - 用 LeaseStore 的 TryAcquire：容忍粒度 = **一个轮询间隔**。窗口内旧主
//     可能仍在执行动作，动作必须幂等。
//   - 用本接口取 token：容忍粒度 = **一次写事务**。动作落库带
//     `WHERE ... AND generation <= $token`，易主后旧主的写被数据库原子拒绝。
//
// 实现方**必须**保证 token 单调：同一 holder 续约保持不变、易主 +1、永不回退。
// 「每次获取都 +1」是错实现——它会让持有者在正常续约时作废自己刚排队的写。
type TokenLeaseStore interface {
	// TryAcquireWithToken 原子获取或续约并返回 fencing token。
	// 没抢到 → (false, 0, nil)（token 无意义，不是错误）。
	// 校验口径与 TryAcquire 一致：空 key/holder、非正 ttl 一律报错。
	TryAcquireWithToken(ctx context.Context, key, holder string, ttl time.Duration) (bool, int64, error)
}

// LeaseCleaner 是 LeaseStore 的**可选**运维能力（类型断言探测，不改
// LeaseStore 接口本身，与 TokenLeaseStore 同一道兼容性纪律，oagentkit/queue#84）：
// 删除已过期的租约行，返回删除行数。
//
// 租约行只有两种离开方式：持有者显式 Release，或被 Cleanup 清掉。缺了后者，
// 进程崩溃留下的行会永久占着主键——动态 key（按任务/租户拼 key）场景下表
// 只增不减（queue#33）。清理判据必须走 **DB 时钟**且与过期判定同钟（自实现
// 者红线，见 sqllease.Store.Cleanup 与 ExpiredPredicate 的文档），不得绑应用
// 时钟 cutoff——那会让时钟偏慢的副本删掉还活着的租约，双主窗口凭空打开。
//
// 宿主侧统一入口（不关心具体实现时）：
//
//	if lc, ok := store.(worker.LeaseCleaner); ok {
//		n, err := lc.Cleanup(ctx) // 随心跳/定时任务周期性调用
//	}
//
// 参考实现：sqllease.Store（三方言判据同源 ExpiredPredicate）与
// pglease.PGLeaseStore（now()）。
type LeaseCleaner interface {
	// Cleanup 删除已过期的租约行，返回删除行数。判定必须用 DB 时钟（与
	// TryAcquire 的过期判定同钟），且为严格小于（`<`）——`<=` 会删掉边界上
	// 活着的租约。
	Cleanup(ctx context.Context) (int64, error)
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

	mu          sync.Mutex
	leader      bool
	everStarted bool          // Start 是否被调用过（Stop 是否该动手，queue#37）
	running     bool          // 竞选 goroutine 在跑（生命周期状态机，queue#96）
	stopClosed  bool          // 本生命周期是否已关闭 stop（Stop 幂等的门，替代 stopOnce——Once 无法随生命周期重置）
	stop        chan struct{} // Stop 请求信号
	// done 竞选定稿且回调已排空（限时）后关闭——Stop 等它，故「Stop 返回」
	// 同时意味着「不再翻转角色」与「已入队的回调都跑过了」。
	done   chan struct{}
	cbCh   chan func()   // 角色翻转回调队列：竞选 goroutine 是唯一写者
	cbDone chan struct{} // 回调调度 goroutine 退出后关闭
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
//
// 参数在**构造期 fail-fast**（panic）：ttl/interval 非正的选主器语义已破
// （ticker panic / 租约永不过期），而启动后 panic 发生在后台竞选 goroutine
// 里（oagentkit/queue#27——进程直接被炸，调用方毫无拦截机会），必须在构造
// 期拦截。参数来自配置文件的宿主在装配期即得到反馈，不是运行期。
func NewLeaderElector(store LeaseStore, key, holder string, ttl, interval time.Duration,
	opts ...LeaderOption) *LeaderElector {
	// nil store 与非正 ttl 同一道闸门（oagentkit/queue#71）：构造期放行的
	// nil LeaseStore 会在首个 tick 对 nil 接口调方法——panic 落在后台竞选
	// goroutine（Start 拉起），进程直接被炸，调用方毫无拦截机会。装配期
	// （配置驱动的 store 条件构造）最容易产出 nil，拦截点必须在构造函数。
	if store == nil {
		panic("worker: NewLeaderElector store 不能为 nil——nil LeaseStore 的方法调用会在后台竞选 goroutine 里 panic（oagentkit/queue#27 同型：进程直接被炸，调用方毫无拦截机会），必须在构造期拦截")
	}
	if ttl <= 0 {
		panic(fmt.Sprintf("worker: NewLeaderElector ttl 必须 > 0（得到 %v）——非正 ttl 让「失联后 ≤ttl 换主」承诺失效", ttl))
	}
	if interval <= 0 {
		panic(fmt.Sprintf("worker: NewLeaderElector interval 必须 > 0（得到 %v）——非正 interval 在后台 ticker 处 panic（oagentkit/queue#27）", interval))
	}
	if key == "" {
		panic("worker: NewLeaderElector key 不能为空——空 key 让多副本竞争同一把锁的语义失效")
	}
	// holder 与 key 是一对身份：key 指「哪把锁」，holder 指「谁在持」。
	// 空 holder 不是「没名字」而是**所有副本共用同一个名字**——租约实现的
	// 「本人持有则顺延」条件（`holder = ?` / `holder = EXCLUDED.holder`）对
	// 空串恒真 ⇒ 多副本同时自认 leader，而选举语义零报破产（queue#43）。
	if holder == "" {
		panic("worker: NewLeaderElector holder 不能为空——空 holder 让所有副本在租约层互相「本人持有」互认，双主无任何报错（oagentkit/queue#43）")
	}
	// 续约节拍必须**严格短于**租期（queue#36）：ticker 每 interval 续一次，
	// 租约活 ttl。interval >= ttl 时租约在两次续约之间必然过期——下一次
	// TryAcquire 之前别的副本就能拿走，而本实例的 IsLeader 仍为 true，双主
	// 窗口由此打开，且**零报错**（两边都自认 leader，日志各自安好）。
	//
	// 这条与「ttl<=0 / interval<=0」是同一类构造期闸门：运行时再发现已经晚了
	// （窗口出现在换主的那一刻，事后无法区分「正常的换主」与「双主」）。
	if interval >= ttl {
		panic(fmt.Sprintf("worker: NewLeaderElector interval 必须小于 ttl（ttl=%v interval=%v）——续约节拍不短于租期时，租约在两次续约之间必然过期，其他副本可在本实例仍自认 leader 时接管（双主窗口，oagentkit/queue#36）", ttl, interval))
	}
	e := &LeaderElector{
		store:    store,
		key:      key,
		holder:   holder,
		ttl:      ttl,
		interval: interval,
		log:      logx.Delegating("oagentkit-leader"),
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
// 竞选进行中的重复调用为 no-op（只允许一个竞选 goroutine）。
//
// Stop（或 ctx 取消）之后再 Start 会**重建竞选 goroutine、重新参选**
// （oagentkit/queue#96）：启动流程曾被 startOnce 包住，Stop 消耗 Once 之后
// 第二次 Start 的函数体一次都不执行——不拉起 goroutine、无日志无错误，选主器
// 被静默砖化（其他副本接管租约，本实例自以为还在参选）。改为显式状态机
// （running/stopClosed + 锁）：同一时刻至多一条生命周期，退出后 Start 开新
// 生命周期，通道组随之重建。重复 Start（已在跑）与 Pool.start_ignored 同款
// 口径留 Warn 观测面；重启落 Info（restarted），「已重启」与「被忽略」从此
// 在日志里可区分。
func (e *LeaderElector) Start(ctx context.Context) {
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		// 竞选进行中的重复 Start：文档化 no-op 契约（只允许一个竞选
		// goroutine），但不再静默——留观测面供误重启排查（与 pool.go
		// start_ignored 同款口径，oagentkit/queue#96）。
		e.log.Warn("agentkit.leader.start_ignored", "key", e.key, "reason", "already_running")
		return
	}
	// everStarted 同步置位（在拉起 goroutine 之前）：Stop 若在 goroutine
	// 尚未起来时读到 false 会直接返回，既不等待也不让位，租约泄漏。
	//
	// 置位与 Stop 的读在同一把锁内：否则存在「Stop 读到 false 当成未启动
	// no-op，Start 紧接着照常参选」的窗口——调用方以为已停机，选主器还在跑。
	restarted := e.everStarted
	e.everStarted = true
	e.running = true
	e.stopClosed = false
	e.stop = make(chan struct{})
	e.done = make(chan struct{})
	e.cbCh = make(chan func(), callbackQueue)
	e.cbDone = make(chan struct{})
	// 生命周期通道组以局部变量捕获：竞选 goroutine 只触碰自己这轮的通道，
	// 与下一轮 Start 的重建互不干扰（running 清零是旧 goroutine 的最后一步，
	// 新生命周期在它之后才可能开——见下方 defer 的次序说明）。
	stopCh, doneCh, cbCh, cbDone := e.stop, e.done, e.cbCh, e.cbDone
	e.mu.Unlock()

	if restarted {
		e.log.Info("agentkit.leader.restarted", "key", e.key, "holder", e.holder)
	}

	// 回调调度 goroutine：与竞选循环解耦（queue#41），范围读走完 cbCh
	// 即退出。单个回调的 panic 在 runCallback 内收编，不打死调度器。
	async.GoSafe(func() {
		defer close(cbDone)
		for f := range cbCh {
			e.runCallback(f)
		}
	})
	// GoWithContext（ekit v0.29 补齐的携带 ctx 安全 goroutine）：tick 与
	// select 消费同一个显式传入的 ctx
	async.GoWithContext(ctx, func(ctx context.Context) {
		// defer LIFO——退出收尾次序有讲究（queue#96）：先 finishCampaign
		// （封回调队列 → 排空 → 关 done，Stop 的等待点由此解除），**最后**
		// 清 running。running 清零是本 goroutine 的最后一步：Start 的重启
		// 分支只可能在它之后替换通道组，旧生命周期的 tick/yield/回调入队
		// 因此绝不会摸到新一轮的 stop/cbCh。
		defer func() {
			e.mu.Lock()
			e.running = false
			e.mu.Unlock()
		}()
		defer e.finishCampaign(cbCh, cbDone, doneCh)
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
			case <-stopCh:
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
}

// IsLeader 当前是否持有领导权（最近一次续约结果）。
func (e *LeaderElector) IsLeader() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.leader
}

// Stop 停止竞选并主动让位（幂等；优雅停机的计划内交接，不触发 onLost）。
// 等待竞选 goroutine 退出后返回——返回后 IsLeader 必为 false 且租约已让出。
// Start 未被调用时立即返回（无租约可让），且**不消耗停机状态**：此后 Start
// 依然能正常参选，之后真正的 Stop 也依然有效。停机完成后再次 Start 可重启
// 竞选（oagentkit/queue#96，状态机语义见 Start 文档）。
//
// 「不消耗停机状态」是本方法需要小心的地方（oagentkit/queue#37）：曾无条件
// `close(e.stop)`，于是「Start 之前误调一次 Stop」就把 startOnce 与 stop 一起
// 烧掉——之后的 Start 其竞选 goroutine 首个 tick 即 stopping()==true 直接退出，
// 选主器永不参选、IsLeader() 恒 false，无日志无错误。这是「误调用一次即静默
// 砖化」，与 Pool.Stop 的「未启动时 no-op，真正的 Stop 依然有效」语义相反。
func (e *LeaderElector) Stop() {
	e.mu.Lock()
	if !e.everStarted {
		// 未启动：no-op，不关闭 e.stop（关闭即砖化后续的 Start）
		e.mu.Unlock()
		return
	}
	// done 必须与 close(stop) 在同一把锁内捕获：Stop 的等待目标必须是**自己
	// 停掉的这一轮生命周期**——否则 Stop 与 Start（重启）并发时，重启已把
	// e.done 换成新一轮的通道，本 Stop 会挂在新轮的 done 上等一个不该由
	// 它等的结果。
	done := e.done
	if !e.stopClosed {
		close(e.stop)
		e.stopClosed = true
	}
	e.mu.Unlock()
	<-done
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

// callbackQueue 角色翻转回调队列容量。**只装翻转事件**：稳定复用期间一个 tick
// 最多产出一个状态，且只在 true/false 真正变化时入队。64 远超任何现实窗口内的
// 翻转次数——填满说明回调已经卡死。
const callbackQueue = 64

// callbackDrainTimeout 停机时等待回调排空的限时：回调是通知，不是停机屏障，
// 卡死的回调不能把 Stop 拖成无界等待。
const callbackDrainTimeout = 5 * time.Second

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

// finishCampaign 竞选 goroutine 的退出收尾：先封住回调队列，再等调度器排空，
// 最后关 done。通道一律来自参数（本轮生命周期的局部捕获）——重启后 e.cbCh
// 等字段已被新一轮 Start 换掉，收尾必须关自己那一轮的通道而不是字段里的
// 新通道（oagentkit/queue#96）。
//
// 顺序有含义：done 是 Stop 唯一的等待点，所以它必须表示「不再翻转角色**且**
// 已入队的回调都跑过了」——否则调用方在 Stop 之后读到的组件状态仍是「还没收到
// onLost」的旧值，而那正是它指望 Stop 保证的事。
func (e *LeaderElector) finishCampaign(cbCh chan func(), cbDone, done chan struct{}) {
	// 本 goroutine 是 cbCh 的唯一写者（setLeader 只从 tick 里调），
	// 故此处 close 不会与入队撞上。
	close(cbCh)
	select {
	case <-cbDone:
	case <-time.After(callbackDrainTimeout):
		e.log.Warn("agentkit.leader.callback_drain_timeout",
			"key", e.key, "waited", callbackDrainTimeout.String(),
			"action", "角色翻转回调未在限期内返回，Stop 不再等它——回调 goroutine 仍在运行，其组件状态可能滞后")
	}
	close(done)
}

// enqueueCallback 把角色翻转回调交给调度 goroutine（queue#41）。
//
// 回调曾经在 setLeader 内**同步**执行：一个慢回调（等外部服务、做重活）会把
// 下一次 tick 顶后，租约在回调执行期间过期、其他副本接管——双主窗口从
// 「≤一个轮询间隔」扩大成「回调耗时 + 一个轮询间隔」，而本实例的 IsLeader
// 仍为 true。改成入队后，续约节拍与回调耗时不再互相决定。
//
// 反压方向是刻意选的：队列满时**丢通知**而不是阻塞竞选循环。回调卡死时
// 阻塞入队等于把回调的故障重新传染给续约——正是这条要拆掉的耦合。
func (e *LeaderElector) enqueueCallback(f func()) {
	if f == nil {
		return
	}
	select {
	case e.cbCh <- f:
	default:
		e.log.Warn("agentkit.leader.callback_dropped",
			"key", e.key, "reason", "callback_queue_full",
			"action", "回调卡死，丢弃本次角色翻转通知；续约不受影响")
	}
}

// runCallback 执行单个回调并收编 panic：调度 goroutine 是唯一的回调执行者，
// 它死了就再也不会有任何回调——用例/宿主的 panic 不能升级成「选主通知永久静默」。
func (e *LeaderElector) runCallback(f func()) {
	defer func() {
		if r := recover(); r != nil {
			e.log.Warn("agentkit.leader.callback_panic", "key", e.key, "panic", fmt.Sprint(r))
		}
	}()
	f()
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
		e.enqueueCallback(e.onGained)
	} else if !v && prev {
		e.log.Warn("agentkit.leader.lost", "key", e.key, "holder", e.holder)
		e.enqueueCallback(e.onLost)
	}
}
