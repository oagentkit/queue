package worker

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oekit/logx"
)

// memLeaseStore 内存租约存储（单测；生产实现 = SQL 条件 UPSERT）。
type memLeaseStore struct {
	mu        sync.Mutex
	holder    map[string]string
	expiresAt map[string]time.Time
	now       func() time.Time
}

func newMemLeaseStore() *memLeaseStore {
	return &memLeaseStore{holder: map[string]string{}, expiresAt: map[string]time.Time{}, now: time.Now}
}

func (m *memLeaseStore) TryAcquire(_ context.Context, key, holder string, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if h, ok := m.holder[key]; ok && h != holder && now.Before(m.expiresAt[key]) {
		return false, nil
	}
	m.holder[key] = holder
	m.expiresAt[key] = now.Add(ttl)
	return true, nil
}

func (m *memLeaseStore) Release(_ context.Context, key, holder string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.holder[key] == holder {
		delete(m.holder, key)
		delete(m.expiresAt, key)
	}
	return nil
}

func TestLeaderElectorGainedAndHandover(t *testing.T) {
	store := newMemLeaseStore()
	var gainedA, lostA int
	a := NewLeaderElector(store, "poller", "A", 200*time.Millisecond, 50*time.Millisecond,
		WithOnGained(func() { gainedA++ }), WithOnLost(func() { lostA++ }),
		WithLeaderLogger(logx.NewSlogLogger("test")))
	ctx, cancel := context.WithCancel(context.Background())
	a.Start(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !a.IsLeader() {
		time.Sleep(10 * time.Millisecond)
	}
	if !a.IsLeader() {
		t.Fatal("A 应获得领导权")
	}

	// B 加入：租约未过期不得抢走
	b := NewLeaderElector(store, "poller", "B", 200*time.Millisecond, 20*time.Millisecond,
		WithLeaderLogger(logx.NewSlogLogger("test")))
	b.Start(ctx)
	defer b.Stop()
	time.Sleep(150 * time.Millisecond)
	if b.IsLeader() || !a.IsLeader() {
		t.Fatalf("租约在途 B 不得当选: a=%v b=%v", a.IsLeader(), b.IsLeader())
	}

	// A 停止让位 → B 应当选
	a.Stop()
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !b.IsLeader() {
		time.Sleep(10 * time.Millisecond)
	}
	if !b.IsLeader() {
		t.Fatal("A 让位后 B 应当选")
	}
	if lostA != 0 {
		t.Fatalf("Stop 主动让位不应触发 onLost: %d", lostA)
	}
	cancel()
}

func TestLeaderElectorExpiryHandover(t *testing.T) {
	store := newMemLeaseStore()
	now := time.Now()
	store.now = func() time.Time { return now } // 冻结时钟，手动推进

	a := NewLeaderElector(store, "poller", "A", time.Minute, 10*time.Millisecond,
		WithLeaderLogger(logx.NewSlogLogger("test")))
	ctx := context.Background()
	a.Start(ctx)
	defer a.Stop()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !a.IsLeader() {
		time.Sleep(5 * time.Millisecond)
	}
	if !a.IsLeader() {
		t.Fatal("A 应当选")
	}

	// A 失联：时钟跳过 ttl，B 竞争应换主（A 不再续约后其 IsLeader 在下次 tick 翻转）
	now = now.Add(2 * time.Minute)
	b := NewLeaderElector(store, "poller", "B", time.Minute, 10*time.Millisecond,
		WithLeaderLogger(logx.NewSlogLogger("test")))
	b.Start(ctx)
	defer b.Stop()
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !b.IsLeader() {
		time.Sleep(5 * time.Millisecond)
	}
	if !b.IsLeader() {
		t.Fatal("租约过期后 B 应换主当选")
	}
}

// gatedLeaseStore 首次 TryAcquire 阻塞到 gate 关闭（模拟秒级存储调用，
// 制造"Stop 与在途 tick 穿插"窗口）。
type gatedLeaseStore struct {
	memLeaseStore
	gate  chan struct{}
	first atomic.Bool
}

func (s *gatedLeaseStore) TryAcquire(ctx context.Context, key, holder string, ttl time.Duration) (bool, error) {
	if s.first.CompareAndSwap(true, false) {
		<-s.gate
	}
	return s.memLeaseStore.TryAcquire(ctx, key, holder, ttl)
}

func TestLeaderElectorStopWithInflightTick(t *testing.T) {
	// Stop 到达时首次 tick 在途：在途 acquire 完成后不得宣布当选（onGained 不触发），
	// 刚获得的租约必须立即让出；Stop 返回后 IsLeader=false 且存储中无本持有者租约。
	store := &gatedLeaseStore{memLeaseStore: *newMemLeaseStore(), gate: make(chan struct{})}
	store.first.Store(true) // 首次 TryAcquire 阻塞在 gate
	var gained int
	e := NewLeaderElector(store, "poller", "A", time.Minute, 10*time.Millisecond,
		WithOnGained(func() { gained++ }), WithLeaderLogger(logx.NewSlogLogger("test")))
	e.Start(context.Background())
	time.Sleep(50 * time.Millisecond) // 等竞选 goroutine 阻塞在首次 TryAcquire 的 gate 上

	// 先确保 stop 已关闭，再放行在途 tick：acquire 将在 stopping 状态下返回成功。
	// 同包白盒：close(e.stop) 等价于 Stop() 的第一步；随后等 e.done 即
	// Stop() 的等待段（close+wait 分开写以绕开 stopOnce 重复 close）
	close(e.stop)
	close(store.gate)
	select {
	case <-e.done:
	case <-time.After(2 * time.Second):
		t.Fatal("让位应在在途 tick 结束后完成（Stop 返回条件）")
	}
	if e.IsLeader() {
		t.Fatal("Stop 后 IsLeader 应为 false")
	}
	if gained != 0 {
		t.Fatalf("stop 后在途 acquire 不得宣布当选: onGained 触发 %d 次", gained)
	}
	store.mu.Lock()
	holder, held := store.holder["poller"]
	store.mu.Unlock()
	if held && holder == "A" {
		t.Fatal("stop 后在途获得的租约应已让出")
	}
}

// TestLeaderElectorStopBoundedWhenAcquireHangs 回归（v0.10.28 审计）：TryAcquire
// 挂起（存储层故障/网络分区且 driver 无超时）时 Stop 不得永久阻塞——tick 的
// acquire 现带 5s 独立限时，挂起场景下 Stop 有界返回。
type hangingStore struct {
	memLeaseStore
	hang chan struct{}
}

func (s *hangingStore) TryAcquire(ctx context.Context, key, holder string, ttl time.Duration) (bool, error) {
	select {
	case <-s.hang: // 永不关闭：模拟存储挂起，直到 ctx 取消
	case <-ctx.Done():
		return false, ctx.Err()
	}
	return s.memLeaseStore.TryAcquire(ctx, key, holder, ttl)
}

func TestLeaderElectorStopBoundedWhenAcquireHangs(t *testing.T) {
	store := &hangingStore{memLeaseStore: *newMemLeaseStore(), hang: make(chan struct{})}
	e := NewLeaderElector(store, "poller", "A", time.Minute, 10*time.Millisecond,
		WithLeaderLogger(logx.NewSlogLogger("test")))
	e.Start(context.Background())
	time.Sleep(50 * time.Millisecond) // 等竞选 goroutine 进入挂起的 TryAcquire

	done := make(chan struct{})
	go func() {
		e.Stop()
		close(done)
	}()
	select {
	case <-done:
		// Stop 有界返回（acquire 限时 5s < 本测试等待预算）
	case <-time.After(8 * time.Second):
		t.Fatal("TryAcquire 挂起时 Stop 应有界返回（acquire 独立限时）")
	}
	if e.IsLeader() {
		t.Fatal("挂起期间不得当选")
	}
}
