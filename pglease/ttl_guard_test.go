// 外部测试包（pglease_test）：同 pglease_test.go 的理由——worker re-export
// pglease，包内测试再引 worker 会成环。
package pglease_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/oagentkit/queue/pglease"
)

// TestNonPositiveTTLRejectedBeforeDB 非正 ttl 必须在**触库之前**被拒（queue#35）。
//
// 失效形态：`expires_at` 由 `now() + ttl` 算出，ttl ≤ 0 时租约**落库即过期**
// ——下一次 TryAcquire 立刻判「已过期」并可被任何副本抢走，而「同一时刻至多
// 一个 leader」正是这个包存在的理由。失效还是静默的：选举每次都「成功」，
// 只是从来不是独占。配置漏填（JSON/env 零值）是最常见的触发面。
//
// 与空身份用例同款手法：给一个**连不上**的 DSN，校验在位则在建立连接之前返回
// 本包哨兵错误；漏校验则报连接错误 ⇒ 断言红。用例既不需要真库，又能区分
// 「报错」与「报对了错」。
func TestNonPositiveTTLRejectedBeforeDB(t *testing.T) {
	db, err := sql.Open("postgres", "host=127.0.0.1 port=1 user=x dbname=x sslmode=disable connect_timeout=1")
	if err != nil {
		t.Fatalf("sql.Open 只解析 DSN，不该失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := pglease.NewPGLeaseStore(db)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	for _, ttl := range []time.Duration{0, -time.Second, -time.Hour} {
		ok, err := s.TryAcquire(ctx, "lead", "a", ttl)
		if !errors.Is(err, pglease.ErrInvalidTTL) {
			t.Fatalf("ttl=%v 应返回 ErrInvalidTTL，得到 ok=%v err=%v——走到 SQL 即落库即过期", ttl, ok, err)
		}
		if ok {
			t.Fatalf("ttl=%v 不得被判定为获取成功", ttl)
		}
	}
}

// TestPositiveTTLStillPassesGuard 成对对照：正 ttl 不得被新校验误拦——否则
// 「一律拒绝」也能让上一条绿，而选举完全不可用。
func TestPositiveTTLStillPassesGuard(t *testing.T) {
	db, err := sql.Open("postgres", "host=127.0.0.1 port=1 user=x dbname=x sslmode=disable connect_timeout=1")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := pglease.NewPGLeaseStore(db)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// 正 ttl 应继续往下一步走（这里必然以连接错误告终——那不是 ErrInvalidTTL）。
	_, err = s.TryAcquire(ctx, "lead", "a", time.Minute)
	if errors.Is(err, pglease.ErrInvalidTTL) {
		t.Fatalf("正 ttl 不该被 ErrInvalidTTL 拦下: %v", err)
	}
	if err == nil {
		t.Fatal("连不上的 DSN 不该成功——用例前提失效，请检查网络环境")
	}
}
