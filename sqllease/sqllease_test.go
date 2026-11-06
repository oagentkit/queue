package sqllease

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动（测试依赖，同 lib/pq 的先例：零生产依赖）
)

func testStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", "file:memdb?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1) // :memory: 每连接独立库——共享缓存 + 单连接
	t.Cleanup(func() { _ = db.Close() })
	s := New(db, SQLite)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

// 五语义点（pglease 集成测试同款清单）：空闲获取/本人续约/他人持有拒/
// 过期可夺/非持有者 Release no-op。
func TestTryAcquireSemantics(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	ok, err := s.TryAcquire(ctx, "lead", "a", 10*time.Second)
	if err != nil || !ok {
		t.Fatalf("空闲获取: ok=%v err=%v", ok, err)
	}
	ok, err = s.TryAcquire(ctx, "lead", "a", 10*time.Second)
	if err != nil || !ok {
		t.Fatalf("本人续约: ok=%v err=%v", ok, err)
	}
	ok, err = s.TryAcquire(ctx, "lead", "b", 10*time.Second)
	if err != nil || ok {
		t.Fatalf("他人持有应拒: ok=%v err=%v", ok, err)
	}
	if err := s.Release(ctx, "lead", "c"); err != nil {
		t.Fatalf("非持有者 Release 应 no-op nil: %v", err)
	}
	// 过期可夺：写一条已过期的租约（DB 时钟判定的存在性靠这个验证——
	// 应用时钟写入 + DB 判定的组合会在这里漏）
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO agentkit_lease (key, holder, expires_at) VALUES ('stale', 'old', strftime('%s','now') - 1)`); err != nil {
		t.Fatal(err)
	}
	ok, err = s.TryAcquire(ctx, "stale", "b", 10*time.Second)
	if err != nil || !ok {
		t.Fatalf("过期应可夺: ok=%v err=%v", ok, err)
	}
}

// Release 仅持有者本人生效（让位语义）：本人释放后他人立即可取。
func TestReleaseOnlyHolder(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	if _, err := s.TryAcquire(ctx, "lead", "a", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ctx, "lead", "a"); err != nil {
		t.Fatal(err)
	}
	ok, err := s.TryAcquire(ctx, "lead", "b", time.Minute)
	if err != nil || !ok {
		t.Fatalf("让位后他人应立即可取: ok=%v err=%v", ok, err)
	}
}

// 并发竞争：N goroutine 抢同一 key，恰一个 winner（-race 下跑）——
// 「同一时刻至多一个 leader」正确性红线。
func TestConcurrentTryAcquireSingleWinner(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	const n = 16
	var mu sync.Mutex
	winners := map[string]int{}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			holder := fmt.Sprintf("inst-%d", i)
			ok, err := s.TryAcquire(ctx, "lead", holder, time.Minute)
			if err != nil {
				t.Errorf("TryAcquire: %v", err)
				return
			}
			if ok {
				mu.Lock()
				winners[holder]++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if len(winners) != 1 {
		t.Fatalf("应恰一个 winner，得到 %v", winners)
	}
}

// 竞态 INSERT 冲突路径：并发首抢必走「INSERT 冲突 → 回退条件 UPDATE」——
// 无错误外露、无红字、语义仍单 winner（由 TestConcurrentTryAcquireSingleWinner
// 覆盖判定面；本测试覆盖冷启动并发首抢的收敛）。
func TestConcurrentColdStart(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ok, err := s.TryAcquire(ctx, "cold", fmt.Sprintf("i-%d", i), time.Minute)
			if err != nil {
				errs <- err
				return
			}
			_ = ok
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("冷启动并发首抢不应有错误外露: %v", err)
	}
}

// TestEmptyIdentityRejected 空 key / 空 holder 必须在**拼 SQL 之前**被拒
// （queue#43）：条件 UPDATE 的「本人持有则顺延」是内容比对 `holder = ?`，
// 空串对它恒真 ⇒ 所有空 holder 副本互相认作本人 ⇒ 双主且零报错。
//
// 反向验证锚点：去掉 validateIdentity 调用后，本用例第一条会拿到
// (ok=true, err=nil)——不是「报了个别的错」，而是**选举语义直接破产**。
func TestEmptyIdentityRejected(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:emptyident?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	s := New(db, SQLite)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	// 伪造一行 holder='' 的脏租约（旧版/他实现留下的形态）。
	// 刻意设成**已过期**：这样第四步的合法 holder 能接管它，从而证明校验不误伤；
	// 而第一步的空 holder 在旧实现里靠的是 `holder = ?` 命中（与过期无关）——
	// 过期与否都不影响「双主」这条断言。
	if _, err := db.ExecContext(ctx,
		`INSERT INTO agentkit_lease (key, holder, expires_at) VALUES (?, ?, ?)`,
		"lead", "", time.Now().Add(-time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}

	// 1) 空 holder 抢同一把 key：必须报错，且**不得**命中 holder='' 那行
	ok, err := s.TryAcquire(ctx, "lead", "", time.Minute)
	if !errors.Is(err, ErrEmptyHolder) {
		t.Fatalf("空 holder 应返回 ErrEmptyHolder，得到 ok=%v err=%v——命中即双主", ok, err)
	}
	if ok {
		t.Fatal("空 holder 不得被判定为获取成功（双主）")
	}
	// 脏行必须原封不动（被拒的调用零副作用）
	var holder string
	if err := db.QueryRowContext(ctx,
		`SELECT holder FROM agentkit_lease WHERE key = ?`, "lead").Scan(&holder); err != nil {
		t.Fatalf("脏行应保留: %v", err)
	}
	if holder != "" {
		t.Fatalf("被拒的 TryAcquire 不得改写 holder，得到 %q", holder)
	}

	// 2) 空 key 同理
	if _, err := s.TryAcquire(ctx, "", "a", time.Minute); !errors.Is(err, ErrEmptyKey) {
		t.Fatalf("空 key 应返回 ErrEmptyKey，得到 %v", err)
	}

	// 3) 空 holder 的 Release：DELETE 的 `holder = ?` 同样恒真 ⇒ 会删掉那把
	//    它认不出的锁。必须报错，且行仍在。
	if err := s.Release(ctx, "lead", ""); !errors.Is(err, ErrEmptyHolder) {
		t.Fatalf("空 holder 的 Release 应返回 ErrEmptyHolder，得到 %v", err)
	}
	if err := s.Release(ctx, "", "a"); !errors.Is(err, ErrEmptyKey) {
		t.Fatalf("空 key 的 Release 应返回 ErrEmptyKey，得到 %v", err)
	}
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agentkit_lease WHERE key = ?`, "lead").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("空 holder 的 Release 不得删行，剩余行数 %d", n)
	}

	// 4) 成对正例：合法身份照常工作（校验不误伤）
	if ok, err := s.TryAcquire(ctx, "lead", "a", time.Minute); err != nil || !ok {
		t.Fatalf("合法 holder 应能接管过期/空 holder 的脏行: ok=%v err=%v", ok, err)
	}
	if err := s.Release(ctx, "lead", "a"); err != nil {
		t.Fatalf("合法 Release: %v", err)
	}
}

// TestNonPositiveTTLRejected 非正 ttl 必须在触库之前拒绝（queue#35）。
//
// 失效形态：`expires_at` 由 ttl 算出，ttl ≤ 0 时租约**落库即过期**——下一次
// TryAcquire 立刻判「已过期」并可被任何副本抢走，而「同一时刻至多一个 leader」
// 正是这个包存在的理由；而且失效是全静默的（每次都「获取成功」，从不独占）。
func TestNonPositiveTTLRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	for _, ttl := range []time.Duration{0, -time.Second, -time.Hour} {
		ok, err := s.TryAcquire(ctx, "lead", "a", ttl)
		if !errors.Is(err, ErrInvalidTTL) {
			t.Fatalf("ttl=%v 应返回 ErrInvalidTTL，得到 ok=%v err=%v", ttl, ok, err)
		}
		if ok {
			t.Fatalf("ttl=%v 不得被判定为获取成功", ttl)
		}
	}

	// 负 ttl 被拒后不得留下任何行——校验必须发生在写库之前。
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+DefaultTable).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("被拒的 TryAcquire 不该写库，实得 %d 行", count)
	}
}

// TestNonPositiveTTLDoesNotBreakPositive 成对对照：正 ttl 照常工作。
func TestNonPositiveTTLDoesNotBreakPositive(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	ok, err := s.TryAcquire(ctx, "lead", "a", time.Minute)
	if err != nil || !ok {
		t.Fatalf("正 ttl 应获取成功: ok=%v err=%v", ok, err)
	}
	ok, err = s.TryAcquire(ctx, "lead", "b", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("他人持有期间不得获取成功（双主）")
	}
}
