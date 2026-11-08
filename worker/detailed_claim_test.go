package worker

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// DetailedClaimer（oagentkit/queue#70）：TaskQueue 的可选加强——Pool 在 Queue
// 实现了它时改走 detailed claim，把 attempts 带进领取侧（观测面：毒任务在
// claimed 日志里可见重试计数）；未实现时走原三方法路径，attempts=0，行为不变。
type detailedQueue struct {
	ids      []string
	i        int
	detailed atomic.Int32 // ClaimNextPendingDetailed 被调用次数
	plain    atomic.Int32 // ClaimNextPending 被调用次数
}

func (q *detailedQueue) ClaimNextPending(_ context.Context) (string, bool, error) {
	q.plain.Add(1)
	id, ok := q.next()
	return id, ok, nil
}

func (q *detailedQueue) ClaimNextPendingDetailed(_ context.Context) (string, int, bool, error) {
	q.detailed.Add(1)
	id, ok := q.next()
	return id, 7, ok, nil // attempts=7：可辨识的标记值
}

func (q *detailedQueue) next() (string, bool) {
	if q.i >= len(q.ids) {
		return "", false
	}
	id := q.ids[q.i]
	q.i++
	return id, true
}

func (q *detailedQueue) TouchRunningHeartbeats(context.Context) error { return nil }
func (q *detailedQueue) ResetRunningToPending(context.Context, time.Duration) (int64, error) {
	return 0, nil
}

// 实现了 DetailedClaimer 的 Queue：claim 走 detailed 路径，attempts 透出。
func TestPoolClaimUsesDetailedClaimer(t *testing.T) {
	q := &detailedQueue{ids: []string{"t1"}}
	p := &Pool{Queue: q, Run: func(context.Context, string) error { return nil }, N: 1}
	id, attempts, ok, err := p.claim(context.Background())
	if err != nil || !ok || id != "t1" {
		t.Fatalf("claim = %q attempts=%d ok=%v err=%v", id, attempts, ok, err)
	}
	if attempts != 7 {
		t.Fatalf("attempts = %d, want 7（detailed 标记值）", attempts)
	}
	if q.detailed.Load() != 1 || q.plain.Load() != 0 {
		t.Fatalf("应走 detailed 路径: detailed=%d plain=%d", q.detailed.Load(), q.plain.Load())
	}
}

// 未实现 DetailedClaimer 的 Queue（宿主自实现/既有实现）：走原路径，attempts=0。
func TestPoolClaimFallsBackToPlainClaim(t *testing.T) {
	q := newMockQueue("t1")
	p := &Pool{Queue: q, Run: func(context.Context, string) error { return nil }, N: 1}
	id, attempts, ok, err := p.claim(context.Background())
	if err != nil || !ok || id != "t1" {
		t.Fatalf("claim = %q attempts=%d ok=%v err=%v", id, attempts, ok, err)
	}
	if attempts != 0 {
		t.Fatalf("普通队列 attempts 应为 0: %d", attempts)
	}
}
