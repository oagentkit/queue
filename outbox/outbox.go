package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/oekit/logx"
)

// DefaultTable 缺省 outbox 表名。
const DefaultTable = "agentkit_outbox"

// 列宽上限，与 Migrate 的 DDL 由同一组常量派生（sqlqueue#85 同族纪律）：
// VARCHAR(n) 按**字符**计，判据取 rune 数——逐字节判会把 128 个汉字误拒。
//
//   - kind 超长：MySQL 非 strict 下静默截断，两个不同 kind 塌成同一行；
//   - key 是幂等键（同键重入替换的判据），超长截断会让两个语义不同的
//     副作用共享一个幂等键——前者的重入替换掉后者的待执行行。
const (
	MaxKindLen = 64
	MaxKeyLen  = 128
)

// 规格零值（Concurrency/Base/Max/MaxAttempts 任一 <=0）的缺省档。
// 取 notice 类通知的合理档位：2s 起步、60s 封顶、3 次进死信。
const (
	DefaultBase        = 2 * time.Second
	DefaultMax         = 60 * time.Second
	DefaultMaxAttempts = 3
)

// 行状态（宿主直查/监控用；done 等业务终态不入此列——成功即删行，表里
// 只留待执行/执行中/死信三态，表不随成功数无限膨胀）。
const (
	// StatusPending 待执行（入队初态；失败退避与显式延后的落点）。
	StatusPending = "pending"
	// StatusRunning 已被本进程某 worker 认领、handler 执行中（claim_token
	// 是认领凭据；stale 兜底回收的判据是 claimed_at）。
	StatusRunning = "running"
	// StatusDeadLetter 终态：失败次数达 MaxAttempts（或失联回收达上限）的
	// 归宿。claim 只取 pending，死信行退出重试循环；人工重驱 =
	// `UPDATE ... SET status='pending', attempts=0, not_before=<now>`，
	// 同键重新 Enqueue 亦会复活（重入即替换）。
	StatusDeadLetter = "dead_letter"
)

// claimRetries 单轮认领的竞态重试上限：候选被抢 → 换下一候选；上限内全部
// 被抢视作本轮无到期行（下一轮 sweep 再试）。sqlqueue 同款常量。
const claimRetries = 8

// tableIdentRe 表名安全白名单（防拼接注入，sqllease/sqlqueue 同款）。
var tableIdentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ErrUnknownKind 未登记的 kind。Enqueue/Later 对没 Register 过的 kind
// fail-fast（issue 参考实现同名哨兵）：拼错 kind 字符串静默落库 = 一条
// 永远无人执行的死行，必须在入口拦下。
var ErrUnknownKind = errors.New("outbox: 未登记的 kind")

// ErrKeyTooLong key 超过 MaxKeyLen（列宽护栏与 Migrate 的 VARCHAR 同源，
// sqlqueue#85 同族）：超长值到 DB 层，MySQL 非 strict 静默截断成另一个
// 幂等键——重入替换会误伤语义无关的行。
var ErrKeyTooLong = errors.New("outbox: key 超长（上限 128 字符）")

// Spec 单个 kind 的投递规格（宿主策略：机制只认规格不认业务）。零值字段
// 取缺省档（见各字段）。
type Spec struct {
	// Kind 副作用种类标识（如 "push"、"writeback"）。Enqueue/Later 按它
	// 路由；必须非空、唯一、≤ MaxKindLen 字符（Register 违者 panic）。
	Kind string
	// Concurrency 本 kind 的 handler 并发上限（<=0 → 1）。同 kind 多 worker
	// 各自独立认领（claim CAS 保证不双领同一行）。
	Concurrency int
	// Base 失败退避基数（<=0 → DefaultBase）。第 n 次失败后等待
	// Base·2^(n-1)，公式与 oekit/concurrency/retry.Backoff 同源（单源，
	// 不手抄）。
	Base time.Duration
	// Max 失败退避封顶（<=0 → DefaultMax；< Base 时钳到 Base——封顶低于
	// 基数只会让首轮退避后恒取封顶，是配置错误而非有意为之）。
	Max time.Duration
	// MaxAttempts 最大执行次数（<=0 → DefaultMaxAttempts）：第 n 次失败后
	// （n >= MaxAttempts）行进 StatusDeadLetter 终态，退出重试循环。
	// 显式延后（Later/DeferError）不计入。
	MaxAttempts int
}

// Handler 宿主副作用处理器。payload 是 Enqueue 存入的 JSON 字节（宿主自行
// 解码到自己的行结构——机制面不注入宿主专用形参）。handler 必须幂等：
// 投递语义是 at-least-once（doc「投递语义」节）。
//
// 特殊返回：DeferError（经 Defer 构造）= 「推后 ≠ 失败」，只推 not_before、
// 不动 attempts；其余 error 走 per-kind 退避并消耗尝试预算；panic 一律视同
// 失败（被 worker recover 后按退避重投，不会打死 sweep 循环）。
type Handler func(ctx context.Context, payload []byte) error

// DeferError handler 请求「延后重投」的信号错误：只推 not_before，
// 不计失败、不消耗 MaxAttempts 预算。经 Defer 构造；errors.As 可判定。
type DeferError struct {
	// Delay 距离下次可认领的延迟；<=0 视作立即（not_before = now）。
	Delay time.Duration
}

func (e *DeferError) Error() string {
	return fmt.Sprintf("outbox: 副作用延后 %s 重投（不计失败）", e.Delay)
}

// Defer 构造延后信号（handler 返回值）：not_before = now + delay，attempts
// 不动。与 Later 的区别是作用点——Later 在**入队时**指定首投延迟；Defer 在
// **handler 内**把当前行推后（典型：下游限频 429 后按服务端提示延后）。
func Defer(delay time.Duration) error {
	if delay < 0 {
		delay = 0
	}
	return &DeferError{Delay: delay}
}

// Health 三态行数诊断面（hostdiag 直读的快捷入口；更细的排查直接
// SELECT 表，列含义见各状态常量）。
type Health struct {
	// Pending 待执行行数（含退避/延后中尚未到点的）。
	Pending int64
	// Running 执行中行数（含崩溃遗留、等待 stale 兜底回收的）。
	Running int64
	// DeadLetter 死信行数（终态，可直查/人工重驱）。
	DeadLetter int64
}

// Queue 统一副作用 outbox：幂等 upsert 入队 + 规格驱动的认领/退避/死信。
// 非并发安全面仅 Register/Run 装配序列；Enqueue/Later/Health 可并发调用。
type Queue struct {
	db      *sql.DB
	dialect Dialect
	table   string

	staleAfter   time.Duration
	pollInterval time.Duration
	log          logx.Logger

	mu    sync.RWMutex
	kinds map[string]registered

	// Run 的运行闸门（同一 Queue 单跑）与在途收尾。
	muRun    sync.Mutex
	running  bool
	sweeps   sync.WaitGroup
	handlers sync.WaitGroup
}

type registered struct {
	spec Spec
	h    Handler
}

// New 构造（表名用 DefaultTable，WithTable 覆盖；失联兜底窗口、轮询间隔、
// logger 见 With 系列）。
//
// 不需要副本身份（sqlqueue 的 runnerID）：认领凭据是**每次认领随机生成**
// 的 claim_token（成功清账/失败退避都按 token 守卫），副本身份对 outbox
// 语义没有用处——同一行先后两次认领即使同实例也必须视为不同认领。
func New(db *sql.DB, dialect Dialect) *Queue {
	if db == nil {
		panic("outbox: New db 不能为 nil——nil *sql.DB 只会把 panic 推迟到首次用语句的 goroutine（常在后台，调用方毫无拦截机会），必须在构造期拦截")
	}
	return &Queue{
		db:           db,
		dialect:      dialect,
		table:        DefaultTable,
		staleAfter:   defaultStaleAfter,
		pollInterval: defaultPollInterval,
		log:          logx.Delegating("oagentkit-queue"),
		kinds:        make(map[string]registered),
	}
}

// WithTable 指定 outbox 表名（非空且匹配标识符白名单才生效，静默忽略
// 非法值——sqlqueue 同款先例）。
func (q *Queue) WithTable(name string) *Queue {
	if name != "" && tableIdentRe.MatchString(name) {
		q.table = name
	}
	return q
}

// WithStaleAfter 设置 stale 兜底窗口（<=0 忽略）：running 行 claimed_at
// 超过窗口未清账即视作进程失联遗留，回收重投（attempts+1，达上限进死信）。
// 缺省 defaultStaleAfter。⚠️ 必须**大于 handler 最长耗时**——慢 handler 被
// 回收即同键双执行（at-least-once 契约下不丢、但重复）。
func (q *Queue) WithStaleAfter(d time.Duration) *Queue {
	if d > 0 {
		q.staleAfter = d
	}
	return q
}

// WithPollInterval 设置 sweep 轮询间隔（<=0 忽略）：无到期行时 worker 的
// 空转周期。缺省 defaultPollInterval。轮询是**延迟可见**调度（not_before
// 列）的消费节奏——调小只影响 DB 压力，不影响语义。
func (q *Queue) WithPollInterval(d time.Duration) *Queue {
	if d > 0 {
		q.pollInterval = d
	}
	return q
}

// WithLogger 注入组件 logger（nil 忽略）。缺省 logx.Delegating
// ("oagentkit-queue")。认领/清账失败走 Error——账没记上 = 行卡在 running
// 等 stale 兜底，静默会变成「为什么死信数字对不上」。
func (q *Queue) WithLogger(log logx.Logger) *Queue {
	if log != nil {
		q.log = log
	}
	return q
}

// Register 装配一个 kind 的规格与处理器（Run 前完成；Run 之后注册 panic）。
//
// 装配期 fail-fast（panic，与 New 同一道纪律）：空 kind、超长 kind、重复
// kind、nil handler 都是装配期语义破坏——重复注册静默覆盖会让第二个
// handler 接手第一个的全部存量行，错配比缺配更难排查。
//
// 规格零值档：Concurrency<=0 → 1；Base<=0 → DefaultBase；Max<=0 →
// DefaultMax 且 < Base 钳到 Base；MaxAttempts<=0 → DefaultMaxAttempts。
func (q *Queue) Register(spec Spec, h Handler) {
	q.muRun.Lock()
	defer q.muRun.Unlock()
	if q.running {
		panic(fmt.Sprintf("outbox: Register %q 于 Run 之后——sweep 快照已固化，运行中注册的 kind 永远不会被投递（静默死行），装配必须全部完成后才 Run", spec.Kind))
	}
	if spec.Kind == "" {
		panic("outbox: Register kind 不能为空——空 kind 的行永远无人执行，Enqueue 侧虽有 ErrUnknownKind 拦截，装配侧也不该放行")
	}
	if n := utf8.RuneCountInString(spec.Kind); n > MaxKindLen {
		panic(fmt.Sprintf("outbox: Register kind 超长（上限 %d 字符，实得 %d）——MySQL 非 strict 下静默截断成另一个 kind，与 DDL 列宽同源（MaxKindLen）", MaxKindLen, n))
	}
	if h == nil {
		panic(fmt.Sprintf("outbox: Register %q handler 不能为 nil——nil handler 的行被认领后无人执行，只能等 stale 兜底回收，白白烧光重试预算进死信", spec.Kind))
	}
	if spec.Concurrency <= 0 {
		spec.Concurrency = 1
	}
	if spec.Base <= 0 {
		spec.Base = DefaultBase
	}
	if spec.Max <= 0 {
		spec.Max = DefaultMax
	}
	if spec.Max < spec.Base {
		spec.Max = spec.Base
	}
	if spec.MaxAttempts <= 0 {
		spec.MaxAttempts = DefaultMaxAttempts
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, dup := q.kinds[spec.Kind]; dup {
		panic(fmt.Sprintf("outbox: Register kind %q 重复——重复注册静默覆盖会让第二个 handler 接手第一个的全部存量行，错配比缺配更难排查", spec.Kind))
	}
	q.kinds[spec.Kind] = registered{spec: spec, h: h}
}

// Enqueue 幂等入队：同 (kind, key) 重入**替换**而非冲突——payload 更新、
// 状态回 StatusPending、attempts 清零、not_before 重置为当前（死信行同样
// 被复活，最新意图优先）。
//
// fail-fast 顺序（issue 参考实现同序）：nil 队列 → ErrUnknownKind → 空 key
// （幂等键缺失会让重入变成重复入队，恰好破坏 upsert 的全部意义）→
// ErrKeyTooLong → payload JSON 编码。payload 经 json.Marshal 落库，handler
// 收到原始 JSON 字节。
func (q *Queue) Enqueue(ctx context.Context, kind, key string, payload any) error {
	return q.enqueue(ctx, kind, key, payload, 0)
}

// Later 延迟版 Enqueue：not_before = now + delay，到点前 claim 不可见
// （延迟可见调度）。delay<=0 等价 Enqueue；负值视同 0。
//
// 推后 ≠ 失败：Later 不动 attempts——它表达的是「这个副作用就该这时执行」，
// 不是「上次执行失败了」。
func (q *Queue) Later(ctx context.Context, kind, key string, payload any, delay time.Duration) error {
	if delay < 0 {
		delay = 0
	}
	return q.enqueue(ctx, kind, key, payload, delay)
}

func (q *Queue) enqueue(ctx context.Context, kind, key string, payload any, delay time.Duration) error {
	if q == nil {
		return errors.New("outbox: 队列未装配")
	}
	q.mu.RLock()
	_, ok := q.kinds[kind]
	q.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownKind, kind)
	}
	if key == "" {
		return errors.New("outbox: key 不能为空（幂等键缺失会让重入变成重复入队）")
	}
	if n := utf8.RuneCountInString(key); n > MaxKeyLen {
		return fmt.Errorf("%w: 实得 %d 字符", ErrKeyTooLong, n)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("outbox: payload JSON 编码失败（kind %q key %q）: %w", kind, key, err)
	}
	return q.upsertRow(ctx, kind, key, string(body), delay)
}

// Health 三态行数（nil 队列返回零值 + error，与 Enqueue 同一道装配检查）。
func (q *Queue) Health(ctx context.Context) (Health, error) {
	if q == nil {
		return Health{}, errors.New("outbox: 队列未装配")
	}
	return q.countByStatus(ctx)
}
