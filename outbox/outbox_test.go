package outbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动（测试依赖，sqlqueue 同款）
)

// testDBSeq 内存库 DSN 序号（每测独立：cleanup 关闭最后一条连接即销毁
// 共享缓存库；唯一 DSN 防 Run 残留 goroutine 与下一测串库）。
var testDBSeq atomic.Int64

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:memdb_outbox_%d?mode=memory&cache=shared", testDBSeq.Add(1))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1) // :memory: 每连接独立库——共享缓存 + 单连接
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// newTestQueue 构造 + 建表；register 在 Migrate 前完成（装配纪律）。
func newTestQueue(t *testing.T, register func(*Queue)) *Queue {
	t.Helper()
	q := New(testDB(t), SQLite)
	if register != nil {
		register(q)
	}
	if err := q.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return q
}

// nopHandler 空成功 handler。
func nopHandler(context.Context, []byte) error { return nil }

// rowOf 直读行状态（诊断断言用；列语义见各状态常量）。
func rowOf(t *testing.T, q *Queue, kind, key string) (status string, attempts int, payload string) {
	t.Helper()
	err := q.db.QueryRow(`SELECT status, attempts, payload FROM agentkit_outbox
WHERE kind = ? AND dedup_key = ?`, kind, key).Scan(&status, &attempts, &payload)
	if err != nil {
		t.Fatalf("rowOf(%s,%s): %v", kind, key, err)
	}
	return status, attempts, payload
}

// rowCount 表内行数。
func rowCount(t *testing.T, q *Queue) int {
	t.Helper()
	var n int
	if err := q.db.QueryRow(`SELECT COUNT(*) FROM agentkit_outbox`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// backdate 把行的 not_before 回拨 secs 秒（SQLite strftime 整秒粒度）。
func backdate(t *testing.T, q *Queue, secs int) {
	t.Helper()
	_, err := q.db.Exec(`UPDATE agentkit_outbox SET not_before = strftime('%s','now') - ?`, secs)
	if err != nil {
		t.Fatal(err)
	}
}

// backdateClaimed 把 running 行的 claimed_at 回拨（模拟失联遗留）。
func backdateClaimed(t *testing.T, q *Queue, secs int, kind, key string) {
	t.Helper()
	_, err := q.db.Exec(`UPDATE agentkit_outbox SET claimed_at = strftime('%s','now') - ?
WHERE kind = ? AND dedup_key = ?`, secs, kind, key)
	if err != nil {
		t.Fatal(err)
	}
}

// waitFor 轮询等待条件达成（Run 集成测试的收敛判据）。
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", what)
}

// TestEnqueueUpsertReplace 三件套之一：同 (kind, key) 重入替换——payload
// 更新、状态回 pending、attempts 清零、认领凭据清空；死信行同样复活。
func TestEnqueueUpsertReplace(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t, func(q *Queue) { q.Register(Spec{Kind: "push"}, nopHandler) })

	card := map[string]string{"text": "v1"}
	if err := q.Enqueue(ctx, "push", "k", card); err != nil {
		t.Fatal(err)
	}
	status, attempts, payload := rowOf(t, q, "push", "k")
	if status != StatusPending || attempts != 0 || payload != `{"text":"v1"}` {
		t.Fatalf("首投行 = (%s,%d,%s)", status, attempts, payload)
	}
	// 折腾到「执行过、失败过、还有残留认领凭据」的最坏状态再重入
	if _, err := q.db.Exec(`UPDATE agentkit_outbox SET attempts = 5, status = 'dead_letter',
claim_token = 'stale-token', claimed_at = strftime('%s','now')`); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(ctx, "push", "k", map[string]string{"text": "v2"}); err != nil {
		t.Fatal(err)
	}
	status, attempts, payload = rowOf(t, q, "push", "k")
	if status != StatusPending || attempts != 0 || payload != `{"text":"v2"}` {
		t.Fatalf("重入替换后 = (%s,%d,%s)，want (pending,0,v2)", status, attempts, payload)
	}
	if n := rowCount(t, q); n != 1 {
		t.Fatalf("同键重入后行数 = %d, want 1（替换不是新增）", n)
	}
	// 不同 key 是新行
	if err := q.Enqueue(ctx, "push", "k2", card); err != nil {
		t.Fatal(err)
	}
	if n := rowCount(t, q); n != 2 {
		t.Fatalf("不同 key 后行数 = %d, want 2", n)
	}
}

// TestEnqueueGates 入口 fail-fast（issue 参考实现同序）。
func TestEnqueueGates(t *testing.T) {
	ctx := context.Background()
	var nilQ *Queue
	if err := nilQ.Enqueue(ctx, "push", "k", nil); err == nil || !strings.Contains(err.Error(), "未装配") {
		t.Fatalf("nil 队列 Enqueue = %v", err)
	}
	if _, err := nilQ.Health(ctx); err == nil {
		t.Fatal("nil 队列 Health 应报错")
	}

	q := newTestQueue(t, func(q *Queue) { q.Register(Spec{Kind: "push"}, nopHandler) })
	if err := q.Enqueue(ctx, "nope", "k", nil); !errors.Is(err, ErrUnknownKind) || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("未登记 kind = %v, want ErrUnknownKind", err)
	}
	if err := q.Enqueue(ctx, "push", "", nil); err == nil || !strings.Contains(err.Error(), "幂等键") {
		t.Fatalf("空 key = %v", err)
	}
	long := strings.Repeat("a", MaxKeyLen+1)
	if err := q.Enqueue(ctx, "push", long, nil); !errors.Is(err, ErrKeyTooLong) {
		t.Fatalf("超长 key = %v, want ErrKeyTooLong", err)
	}
	// 边界：恰好 128 rune（ASCII）放行
	if err := q.Enqueue(ctx, "push", strings.Repeat("a", MaxKeyLen), nil); err != nil {
		t.Fatalf("128 ASCII rune 应放行: %v", err)
	}
	// rune 口径：128 个汉字（384 字节）也放行（VARCHAR(n) 按字符计，#85 同族）
	if err := q.Enqueue(ctx, "push", strings.Repeat("汉", MaxKeyLen), nil); err != nil {
		t.Fatalf("128 CJK rune 应放行: %v", err)
	}
	// 不可 JSON 编码的 payload
	if err := q.Enqueue(ctx, "push", "k", make(chan int)); err == nil || !strings.Contains(err.Error(), "JSON") {
		t.Fatalf("chan payload = %v", err)
	}
	// Later 与 Enqueue 同一道闸门
	if err := q.Later(ctx, "nope", "k", nil, time.Second); !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("Later 未登记 kind = %v", err)
	}
}

// TestLaterDeferredVisibility 三件套之二：not_before 延迟可见——到点前
// claim 不可见，到点（含回拨）即可认领；delay<=0 等价 Enqueue。
func TestLaterDeferredVisibility(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t, func(q *Queue) { q.Register(Spec{Kind: "push"}, nopHandler) })

	if err := q.Later(ctx, "push", "k", "v", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := q.claimDue(ctx, "push", 3); err != nil || ok {
		t.Fatalf("未到点即可认领: ok=%v err=%v", ok, err)
	}
	var nb float64
	if err := q.db.QueryRow(`SELECT not_before FROM agentkit_outbox WHERE dedup_key='k'`).Scan(&nb); err != nil {
		t.Fatal(err)
	}
	var now float64
	if err := q.db.QueryRow(`SELECT strftime('%s','now')`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	if nb < now+29 { // 秒粒度容差
		t.Fatalf("not_before = %v, want ≥ now+29", nb)
	}
	backdate(t, q, 60) // 到点
	row, ok, err := q.claimDue(ctx, "push", 3)
	if err != nil || !ok {
		t.Fatalf("到点后认领失败: ok=%v err=%v", ok, err)
	}
	if row.key != "k" || string(row.payload) != `"v"` {
		t.Fatalf("认领行 = (%s,%s)", row.key, row.payload)
	}
	// delay<=0 即时可见
	if err := q.Enqueue(ctx, "push", "k0", "v0"); err != nil {
		t.Fatal(err)
	}
	if err := q.Later(ctx, "push", "k1", "v1", 0); err != nil {
		t.Fatal(err)
	}
	if err := q.Later(ctx, "push", "k2", "v2", -time.Second); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"k0", "k1", "k2"} {
		backdate(t, q, 0) // k 已 running，不受影响
		row, ok, err := q.claimDue(ctx, "push", 3)
		if err != nil || !ok || row.key != key {
			t.Fatalf("delay<=0 应即时可见，认领 = (%s,%v,%v)，want %s", keyOf(row), ok, err, key)
		}
	}
}

func keyOf(r *claimRow) string {
	if r == nil {
		return "<nil>"
	}
	return r.key
}

// TestClaimOrdering 候选序：not_before 早者先，同刻按 created_at/dedup_key；
// 未到点的行绝不插队。
func TestClaimOrdering(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t, func(q *Queue) { q.Register(Spec{Kind: "push"}, nopHandler) })

	// b 延迟 2s；a/c 即时（同秒入库，键序裁决：a < c）
	if err := q.Later(ctx, "push", "b", nil, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"c", "a"} {
		if err := q.Enqueue(ctx, "push", k, nil); err != nil {
			t.Fatal(err)
		}
	}
	row, ok, err := q.claimDue(ctx, "push", 3)
	if err != nil || !ok || row.key != "a" {
		t.Fatalf("首认领 = (%s,%v,%v), want a", keyOf(row), ok, err)
	}
	row, ok, err = q.claimDue(ctx, "push", 3)
	if err != nil || !ok || row.key != "c" {
		t.Fatalf("次认领 = (%s,%v,%v), want c", keyOf(row), ok, err)
	}
	if _, ok, err := q.claimDue(ctx, "push", 3); err != nil || ok {
		t.Fatalf("b 未到点不应可见: ok=%v err=%v", ok, err)
	}
	backdate(t, q, 3)
	row, ok, err = q.claimDue(ctx, "push", 3)
	if err != nil || !ok || row.key != "b" {
		t.Fatalf("到点后 = (%s,%v,%v), want b", keyOf(row), ok, err)
	}
}

// TestClaimNoDoubleClaim 并发认领互斥：多 goroutine 抢同一批行，成功认领的
// 键集合必须恰好不重不漏（CAS 守卫；SQLite 单连接下竞态窗口窄，但 -race
// 与守卫路径仍被覆盖）。
func TestClaimNoDoubleClaim(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t, func(q *Queue) { q.Register(Spec{Kind: "push"}, nopHandler) })
	const rows = 8
	for i := range rows {
		if err := q.Enqueue(ctx, "push", fmt.Sprintf("k%d", i), nil); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	claimed := make(map[string]bool)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for {
				row, ok, err := q.claimDue(ctx, "push", 3)
				if err != nil || !ok {
					return
				}
				mu.Lock()
				claimed[row.key] = true
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if len(claimed) != rows {
		t.Fatalf("成功认领键数 = %d, want %d（重复或漏认领）", len(claimed), rows)
	}
}

// TestDeferIsNotFailure 推后 ≠ 失败：DeferError 只推 not_before，attempts
// 不动、预算不消耗。
func TestDeferIsNotFailure(t *testing.T) {
	ctx := context.Background()
	var spec Spec
	q := newTestQueue(t, func(q *Queue) {
		q.Register(Spec{Kind: "push", MaxAttempts: 3}, func(context.Context, []byte) error {
			return Defer(60 * time.Second)
		})
		spec = q.snapshotKinds()["push"].spec
	})
	if err := q.Enqueue(ctx, "push", "k", "v"); err != nil {
		t.Fatal(err)
	}
	row, ok, err := q.claimDue(ctx, "push", spec.MaxAttempts)
	if err != nil || !ok {
		t.Fatal("应可认领")
	}
	q.handleRow(ctx, spec, func(context.Context, []byte) error { return Defer(60 * time.Second) }, row)
	status, attempts, _ := rowOf(t, q, "push", "k")
	if status != StatusPending || attempts != 0 {
		t.Fatalf("延后后 = (%s,%d), want (pending,0)——推后不是失败", status, attempts)
	}
	var nb float64
	if err := q.db.QueryRow(`SELECT not_before FROM agentkit_outbox WHERE dedup_key='k'`).Scan(&nb); err != nil {
		t.Fatal(err)
	}
	var now float64
	if err := q.db.QueryRow(`SELECT strftime('%s','now')`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	if nb < now+59 {
		t.Fatalf("延后未生效: not_before=%v, want ≥ now+59", nb)
	}
	// 第二次执行成功 → 清账
	backdate(t, q, 120)
	row2, ok, err := q.claimDue(ctx, "push", spec.MaxAttempts)
	if err != nil || !ok {
		t.Fatal("延后到期应可再认领")
	}
	row2.payload = []byte("ok")
	q.handleRow(ctx, spec, nopHandler, row2)
	if n := rowCount(t, q); n != 0 {
		t.Fatalf("成功后行数 = %d, want 0（清账）", n)
	}
}

// TestReenqueueDuringRunningKeepsLatest at-least-once 角落：执行中的行被
// 同键重入替换后，在途执行的清账落空（token 已清空），最新载荷重投。
func TestReenqueueDuringRunningKeepsLatest(t *testing.T) {
	ctx := context.Background()
	var spec Spec
	q := newTestQueue(t, func(q *Queue) {
		q.Register(Spec{Kind: "push", MaxAttempts: 3}, nopHandler)
		spec = q.snapshotKinds()["push"].spec
	})
	if err := q.Enqueue(ctx, "push", "k", "v1"); err != nil {
		t.Fatal(err)
	}
	row1, ok, err := q.claimDue(ctx, "push", spec.MaxAttempts)
	if err != nil || !ok || string(row1.payload) != `"v1"` {
		t.Fatalf("首认领 = (%s,%v,%v)", keyOf(row1), ok, err)
	}
	// 执行中重入：替换为 v2
	if err := q.Enqueue(ctx, "push", "k", "v2"); err != nil {
		t.Fatal(err)
	}
	// 在途执行成功，但清账守卫落空（token 被重入清空）
	q.handleRow(ctx, spec, nopHandler, row1)
	status, attempts, payload := rowOf(t, q, "push", "k")
	if status != StatusPending || attempts != 0 || payload != `"v2"` {
		t.Fatalf("替换后 = (%s,%d,%s), want (pending,0,v2)", status, attempts, payload)
	}
	// 最新载荷重投并清账
	row2, ok, err := q.claimDue(ctx, "push", spec.MaxAttempts)
	if err != nil || !ok || string(row2.payload) != `"v2"` {
		t.Fatalf("重投 = (%s,%v,%v)", keyOf(row2), ok, err)
	}
	q.handleRow(ctx, spec, nopHandler, row2)
	if n := rowCount(t, q); n != 0 {
		t.Fatalf("重投成功后行数 = %d, want 0", n)
	}
}

// TestFailAfterReplaceIsVoided 失败判定在重入后作废：token 守卫让旧认领的
// 失败退避落空——新意图的预算是新的（attempts 不被旧执行消耗）。
func TestFailAfterReplaceIsVoided(t *testing.T) {
	ctx := context.Background()
	var spec Spec
	q := newTestQueue(t, func(q *Queue) {
		q.Register(Spec{Kind: "push", MaxAttempts: 3}, nopHandler)
		spec = q.snapshotKinds()["push"].spec
	})
	if err := q.Enqueue(ctx, "push", "k", "v1"); err != nil {
		t.Fatal(err)
	}
	row1, ok, err := q.claimDue(ctx, "push", spec.MaxAttempts)
	if err != nil || !ok {
		t.Fatal("应可认领")
	}
	if err := q.Enqueue(ctx, "push", "k", "v2"); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	q.handleRow(ctx, spec, func(context.Context, []byte) error { return boom }, row1)
	status, attempts, payload := rowOf(t, q, "push", "k")
	if status != StatusPending || attempts != 0 || payload != `"v2"` {
		t.Fatalf("旧认领失败后 = (%s,%d,%s), want (pending,0,v2)", status, attempts, payload)
	}
}

// TestRunDeliversAndCleans Run 集成：三条副作用各投递恰好一次，成功即清账。
func TestRunDeliversAndCleans(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	delivered := make(map[string]int)
	q := newTestQueue(t, func(q *Queue) {
		q.Register(Spec{Kind: "push", Concurrency: 2}, func(_ context.Context, payload []byte) error {
			mu.Lock()
			delivered[string(payload)]++
			mu.Unlock()
			return nil
		})
		q.WithPollInterval(10 * time.Millisecond)
	})
	runDone := make(chan struct{})
	go func() { q.Run(ctx); close(runDone) }()

	for _, k := range []string{"a", "b", "c"} {
		if err := q.Enqueue(ctx, "push", k, "p-"+k); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, 3*time.Second, "全部投递并清账", func() bool {
		h, err := q.Health(ctx)
		return err == nil && h == Health{}
	})
	mu.Lock()
	defer mu.Unlock()
	for _, k := range []string{"a", "b", "c"} {
		if delivered[fmt.Sprintf("%q", "p-"+k)] != 1 {
			t.Fatalf("投递计数 = %v, want 各恰一次", delivered)
		}
	}
	cancel()
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Run 未随 ctx 取消返回")
	}
}

// TestRunBackoffAndDeadLetter 失败退避与死信终态：MaxAttempts=2 时恰执行
// 2 次，第 2 次失败进死信，claim 不再取，attempt 计数定格。
func TestRunBackoffAndDeadLetter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls sync.WaitGroup
	// calls.Add 由测试主体按 MaxAttempts 预挂；handler 每次执行 Done。
	calls.Add(2)
	q := newTestQueue(t, func(q *Queue) {
		q.Register(Spec{Kind: "wb", MaxAttempts: 2, Base: 10 * time.Millisecond, Max: 30 * time.Millisecond},
			func(context.Context, []byte) error {
				calls.Done()
				return errors.New("downstream down")
			})
		q.WithPollInterval(10 * time.Millisecond)
	})
	runDone := make(chan struct{})
	go func() { q.Run(ctx); close(runDone) }()

	if err := q.Enqueue(ctx, "wb", "k", nil); err != nil {
		t.Fatal(err)
	}
	waitDone := make(chan struct{})
	go func() { calls.Wait(); close(waitDone) }()
	select {
	case <-waitDone:
	case <-time.After(3 * time.Second):
		t.Fatal("未在预算内执行满 2 次")
	}
	waitFor(t, 2*time.Second, "进死信", func() bool {
		h, err := q.Health(ctx)
		return err == nil && h.DeadLetter == 1 && h == (Health{DeadLetter: 1})
	})
	status, attempts, _ := rowOf(t, q, "wb", "k")
	if status != StatusDeadLetter || attempts != 2 {
		t.Fatalf("死信行 = (%s,%d), want (dead_letter,2)", status, attempts)
	}
	if _, ok, err := q.claimDue(ctx, "wb", 2); err != nil || ok {
		t.Fatalf("死信不应可认领: ok=%v err=%v", ok, err)
	}
	cancel()
	<-runDone
}

// TestRunHandlerPanicIsFailure handler panic 视同失败：recover 后按退避/
// 死信记账，sweep 循环不被打死（毒载荷 panic 进死信，同 kind 的正常载荷
// 照常清账）。
func TestRunHandlerPanicIsFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var poisonCalls atomic.Int64
	q := newTestQueue(t, func(q *Queue) {
		q.Register(Spec{Kind: "wb", MaxAttempts: 1},
			func(_ context.Context, payload []byte) error {
				if string(payload) == `"ok"` {
					return nil
				}
				poisonCalls.Add(1)
				panic("handler exploded")
			})
		q.WithPollInterval(10 * time.Millisecond)
	})
	runDone := make(chan struct{})
	go func() { q.Run(ctx); close(runDone) }()
	if err := q.Enqueue(ctx, "wb", "k", "poison"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "panic 进死信", func() bool {
		h, err := q.Health(ctx)
		return err == nil && h.DeadLetter == 1 && poisonCalls.Load() == 1
	})
	// sweep 循环还活着：同 kind 的正常载荷照常投递并清账
	if err := q.Enqueue(ctx, "wb", "ok", "ok"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, "后续行照常清账", func() bool {
		h, err := q.Health(ctx)
		return err == nil && h == (Health{DeadLetter: 1}) // 死信行留表，无新增 pending/running
	})
	cancel()
	<-runDone
}

// TestStaleResetJanitor stale 兜底：超窗 running 行回收重投（attempts+1），
// 达 MaxAttempts 直接进死信；未超窗的不动。
func TestStaleResetJanitor(t *testing.T) {
	ctx := context.Background()
	var spec Spec
	q := newTestQueue(t, func(q *Queue) {
		q.Register(Spec{Kind: "wb", MaxAttempts: 3}, nopHandler)
		q.WithStaleAfter(30 * time.Second)
		spec = q.snapshotKinds()["wb"].spec
	})
	if err := q.Enqueue(ctx, "wb", "k", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := q.claimDue(ctx, "wb", spec.MaxAttempts); err != nil || !ok {
		t.Fatal("应可认领")
	}
	// 未超窗：回收不动它
	if n, err := q.resetStaleKind(ctx, "wb", spec.MaxAttempts); err != nil || n != 0 {
		t.Fatalf("未超窗回收 = (%d,%v), want (0,nil)", n, err)
	}
	backdateClaimed(t, q, 60, "wb", "k") // 超窗
	if n, err := q.resetStaleKind(ctx, "wb", spec.MaxAttempts); err != nil || n != 1 {
		t.Fatalf("超窗回收 = (%d,%v), want (1,nil)", n, err)
	}
	status, attempts, _ := rowOf(t, q, "wb", "k")
	if status != StatusPending || attempts != 1 {
		t.Fatalf("回收后 = (%s,%d), want (pending,1)", status, attempts)
	}
	// 第二次回收：attempts 1 < M-1=2 → 仍 pending
	if _, ok, err := q.claimDue(ctx, "wb", spec.MaxAttempts); err != nil || !ok {
		t.Fatal("应可再认领")
	}
	backdateClaimed(t, q, 60, "wb", "k")
	if _, err := q.resetStaleKind(ctx, "wb", spec.MaxAttempts); err != nil {
		t.Fatal(err)
	}
	status, attempts, _ = rowOf(t, q, "wb", "k")
	if status != StatusPending || attempts != 2 {
		t.Fatalf("二次回收 = (%s,%d), want (pending,2)", status, attempts)
	}
	// 第三次回收：attempts 2 >= M-1=2 → 死信
	if _, ok, err := q.claimDue(ctx, "wb", spec.MaxAttempts); err != nil || !ok {
		t.Fatal("应可再认领")
	}
	backdateClaimed(t, q, 60, "wb", "k")
	if _, err := q.resetStaleKind(ctx, "wb", spec.MaxAttempts); err != nil {
		t.Fatal(err)
	}
	status, attempts, _ = rowOf(t, q, "wb", "k")
	if status != StatusDeadLetter || attempts != 3 {
		t.Fatalf("三次回收 = (%s,%d), want (dead_letter,3)", status, attempts)
	}
}

// TestHealthCounts Health 聚合面与三态常量值。
func TestHealthCounts(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t, func(q *Queue) { q.Register(Spec{Kind: "push"}, nopHandler) })
	if StatusPending != "pending" || StatusRunning != "running" || StatusDeadLetter != "dead_letter" {
		t.Fatalf("状态常量值漂移: %s/%s/%s", StatusPending, StatusRunning, StatusDeadLetter)
	}
	for _, k := range []string{"a", "b"} {
		if err := q.Enqueue(ctx, "push", k, nil); err != nil {
			t.Fatal(err)
		}
	}
	if h, err := q.Health(ctx); err != nil || h != (Health{Pending: 2}) {
		t.Fatalf("Health = %+v err=%v", h, err)
	}
	if _, err := q.db.Exec(`UPDATE agentkit_outbox SET status='running', claim_token='t',
claimed_at=strftime('%s','now') WHERE dedup_key='a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := q.db.Exec(`UPDATE agentkit_outbox SET status='dead_letter' WHERE dedup_key='b'`); err != nil {
		t.Fatal(err)
	}
	if h, err := q.Health(ctx); err != nil || h != (Health{Running: 1, DeadLetter: 1}) {
		t.Fatalf("Health = %+v err=%v", h, err)
	}
}

// TestRunAndRegisterGates 装配纪律：无 kind 不跑、单跑、Run 后注册禁绝。
func TestRunAndRegisterGates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run 前未 Register：panic
	q := newTestQueue(t, nil)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("未 Register 即 Run 应 panic")
			}
		}()
		q.Run(ctx)
	}()

	q2 := newTestQueue(t, func(q *Queue) { q.Register(Spec{Kind: "push"}, nopHandler) })
	runEntered := make(chan struct{})
	go func() { q2.Run(ctx); close(runEntered) }()
	waitFor(t, time.Second, "Run 进入运行态", func() bool {
		q2.muRun.Lock()
		defer q2.muRun.Unlock()
		return q2.running
	})
	// 重入 Run：panic
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("重入 Run 应 panic")
			}
		}()
		q2.Run(ctx)
	}()
	// Run 后注册：panic
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("Run 后 Register 应 panic")
			}
		}()
		q2.Register(Spec{Kind: "late"}, nopHandler)
	}()
	cancel()
	<-runEntered
	// Run 返回后闸门复位：可重新 Run（重启形态）
	done := make(chan struct{})
	ctx2, cancel2 := context.WithCancel(context.Background())
	go func() { q2.Run(ctx2); close(done) }()
	cancel2()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("重启 Run 未返回")
	}
}

// TestRegisterGates Register 装配期 fail-fast（全 panic 形态）。
func TestRegisterGates(t *testing.T) {
	assertPanic := func(t *testing.T, what string, fn func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Fatalf("%s 应 panic", what)
			}
		}()
		fn()
	}
	assertPanic(t, "空 kind", func() {
		newTestQueue(t, func(q *Queue) { q.Register(Spec{}, nopHandler) })
	})
	assertPanic(t, "超长 kind", func() {
		newTestQueue(t, func(q *Queue) { q.Register(Spec{Kind: strings.Repeat("k", MaxKindLen+1)}, nopHandler) })
	})
	assertPanic(t, "nil handler", func() {
		newTestQueue(t, func(q *Queue) { q.Register(Spec{Kind: "push"}, nil) })
	})
	assertPanic(t, "重复 kind", func() {
		newTestQueue(t, func(q *Queue) {
			q.Register(Spec{Kind: "push"}, nopHandler)
			q.Register(Spec{Kind: "push"}, nopHandler)
		})
	})
	// 边界：恰好 64 rune 放行
	q := newTestQueue(t, func(q *Queue) { q.Register(Spec{Kind: strings.Repeat("k", MaxKindLen)}, nopHandler) })
	if _, ok := q.snapshotKinds()[strings.Repeat("k", MaxKindLen)]; !ok {
		t.Fatal("64 rune kind 应注册成功")
	}
	// 规格零值档归一
	spec := q.snapshotKinds()[strings.Repeat("k", MaxKindLen)].spec
	if spec.Concurrency != 1 || spec.Base != DefaultBase || spec.Max != DefaultMax || spec.MaxAttempts != DefaultMaxAttempts {
		t.Fatalf("零值规格 = %+v", spec)
	}
	// Max < Base 钳到 Base
	q2 := New(testDB(t), SQLite)
	q2.Register(Spec{Kind: "x", Base: 10 * time.Second, Max: time.Second}, nopHandler)
	if s := q2.snapshotKinds()["x"].spec; s.Max != 10*time.Second {
		t.Fatalf("Max<Base 未钳制: %v", s.Max)
	}
}

// TestOptionGates With 系列与构造闸门。
func TestOptionGates(t *testing.T) {
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("nil db 应 panic")
			}
		}()
		New(nil, SQLite)
	}()
	q := New(testDB(t), SQLite)
	q.WithTable("bad name; DROP").WithStaleAfter(0).WithPollInterval(-time.Second).WithLogger(nil)
	if q.table != DefaultTable || q.staleAfter != defaultStaleAfter || q.pollInterval != defaultPollInterval {
		t.Fatalf("非法选项未按静默忽略处理: table=%s stale=%v poll=%v", q.table, q.staleAfter, q.pollInterval)
	}
	q.WithTable("custom_outbox").WithStaleAfter(15 * time.Second).WithPollInterval(10 * time.Millisecond)
	if q.table != "custom_outbox" || q.staleAfter != 15*time.Second || q.pollInterval != 10*time.Millisecond {
		t.Fatalf("合法选项未生效: table=%s stale=%v poll=%v", q.table, q.staleAfter, q.pollInterval)
	}
}
