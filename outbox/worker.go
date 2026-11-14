package outbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/oekit/concurrency/async"
	"github.com/oekit/concurrency/retry"
	"github.com/oekit/logx"
)

// newClaimToken 生成认领凭据（每认领一次一个，清账/退避/延后按它守卫）。
// crypto/rand.Read 自 Go 1.24 起承诺永不返回 error 且填满整个缓冲。
func newClaimToken() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Run 阻塞驱动全部已注册 kind 的投递循环，直到 ctx 取消；返回时所有在途
// handler 已收尾（账务语句经 context.WithoutCancel 执行，取消不打断记账）。
//
// 装配闸门（panic，doc「装配纪律」节）：未 Register 任何 kind、重入 Run。
// 首次使用前先 Migrate（本方法不建表——表缺失的报错点应在装配期，不是
// 后台循环里）。
//
// 每 kind 一个 sweep goroutine：每轮先跑 stale 兜底回收（崩溃遗留的
// running 行），再把到期行填满 Concurrency 个槽位逐条认领投递；无到期行
// 时按 pollInterval 空转。not_before 到点才可见，轮询只是消费节奏。
func (q *Queue) Run(ctx context.Context) {
	if q == nil {
		panic("outbox: Run 队列未装配")
	}
	q.muRun.Lock()
	if len(q.snapshotKinds()) == 0 {
		q.muRun.Unlock()
		panic("outbox: Run 前未 Register 任何 kind——空表装配是配置错误（oagentkit/queue#69 同道：装配期语义破坏必须当场暴露）")
	}
	if q.running {
		q.muRun.Unlock()
		panic("outbox: Run 重入——同一 Queue 只能单跑（双跑 = 同键双执行，at-least-once 下也不该主动制造重复副作用）")
	}
	q.running = true
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel() // Run 返回即释放派生 ctx（调用方 ctx 取消是唯一停机入口）
	q.muRun.Unlock()

	for _, r := range q.snapshotKinds() {
		q.sweeps.Add(1)
		async.GoSafe(func() {
			defer q.sweeps.Done()
			q.sweepKind(runCtx, r.spec, r.h)
		})
	}
	<-runCtx.Done()
	q.sweeps.Wait() // sweep 先退干净（不再产生新的 handler 投递）
	q.handlers.Wait()
	q.muRun.Lock()
	q.running = false
	q.muRun.Unlock()
}

// snapshotKinds 装配面快照（Register 与 sweep 读取的同步点；Run 后注册
// panic，故快照在 Run 生命周期内恒定）。
func (q *Queue) snapshotKinds() map[string]registered {
	q.mu.RLock()
	defer q.mu.RUnlock()
	out := make(map[string]registered, len(q.kinds))
	for k, v := range q.kinds {
		out[k] = v
	}
	return out
}

// sweepKind 单 kind 的投递循环（见 Run）。sem 是本 kind 的并发槽。
func (q *Queue) sweepKind(ctx context.Context, spec Spec, h Handler) {
	sem := make(chan struct{}, spec.Concurrency)
	for ctx.Err() == nil {
		q.janitor(ctx, spec)
		q.drain(ctx, spec, h, sem)
		select {
		case <-ctx.Done():
			return
		case <-time.After(q.pollInterval):
		}
	}
}

// janitor stale 兜底每轮先跑：崩溃/DB 抖动遗留的 running 行没有「下一次
// 失败」可等，只能按 claimed_at 超窗回收。
func (q *Queue) janitor(ctx context.Context, spec Spec) {
	n, err := q.resetStaleKind(ctx, spec.Kind, spec.MaxAttempts)
	if err != nil {
		q.log.Error("agentkit.outbox.stale_reset_failed",
			"kind", spec.Kind, logx.ErrKey, err.Error())
		return
	}
	if n > 0 {
		q.log.Warn("agentkit.outbox.stale_reset", "kind", spec.Kind, "reset", n)
	}
}

// drain 把本 kind 的到期行填满并发槽位：抢到空槽才认领（CAS 双抢败者换
// 下一候选），无槽/无行/认领故障即返回，交给外层轮询节奏。
func (q *Queue) drain(ctx context.Context, spec Spec, h Handler, sem chan struct{}) {
	for ctx.Err() == nil {
		select {
		case sem <- struct{}{}: // 抢到并发槽
		default:
			return // 槽满：在途 handler 未收尾
		}
		row, ok, err := q.claimDue(ctx, spec.Kind, spec.MaxAttempts)
		if err != nil {
			<-sem
			q.log.Error("agentkit.outbox.claim_failed",
				"kind", spec.Kind, logx.ErrKey, err.Error())
			return
		}
		if !ok {
			<-sem
			return // 本轮无更多到期行
		}
		q.handlers.Add(1)
		async.GoSafe(func() {
			defer q.handlers.Done()
			defer func() { <-sem }()
			q.handleRow(ctx, spec, h, row)
		})
	}
}

// handleRow 执行一条已认领的行并记账。handler 的 panic 一律视作失败
// （recover 后走退避重投——handler 崩溃打死 sweep 循环会让整个 kind 停摆，
// 比重投严重得多）。记账语句用 WithoutCancel：ctx 取消（关停）不打断
// 已执行副作用的记账，否则行卡 running 只能等 stale 兜底。
func (q *Queue) handleRow(ctx context.Context, spec Spec, h Handler, row *claimRow) {
	bookCtx := context.WithoutCancel(ctx)
	err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("outbox: handler panic（kind %q key %q）: %v", row.kind, row.key, r)
			}
		}()
		return h(ctx, row.payload)
	}()
	switch {
	case err == nil:
		if _, err := q.completeClaimed(bookCtx, row.kind, row.key, row.token); err != nil {
			q.log.Error("agentkit.outbox.complete_failed",
				"kind", row.kind, "key", row.key, logx.ErrKey, err.Error())
		}
	case isDefer(err):
		d := deferOf(err)
		if _, err := q.deferClaimed(bookCtx, row.kind, row.key, row.token, d); err != nil {
			q.log.Error("agentkit.outbox.defer_failed",
				"kind", row.kind, "key", row.key, logx.ErrKey, err.Error())
		}
	default:
		next := row.attempts + 1
		var bookErr error
		if next >= spec.MaxAttempts {
			_, bookErr = q.deadLetterClaimed(bookCtx, row.kind, row.key, row.token)
		} else {
			backoff := retry.Backoff(retry.Config{
				Delay:       spec.Base,
				MaxDelay:    spec.Max,
				Exponential: true,
			}, next)
			_, bookErr = q.failClaimed(bookCtx, row.kind, row.key, row.token, backoff)
		}
		if bookErr != nil {
			q.log.Error("agentkit.outbox.fail_bookkeeping_failed",
				"kind", row.kind, "key", row.key, logx.ErrKey, bookErr.Error())
		} else if next >= spec.MaxAttempts {
			q.log.Warn("agentkit.outbox.dead_letter",
				"kind", row.kind, "key", row.key, "attempts", next)
		}
	}
}

// isDefer / deferOf 判定并提取延后信号（errors.As 通道，wrap 传递不丢）。
func isDefer(err error) bool {
	var de *DeferError
	return errors.As(err, &de)
}

func deferOf(err error) time.Duration {
	var de *DeferError
	if errors.As(err, &de) {
		if de.Delay < 0 {
			return 0
		}
		return de.Delay
	}
	return 0
}
