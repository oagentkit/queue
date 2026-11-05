package sqllease

import (
	"context"
	"database/sql"
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
