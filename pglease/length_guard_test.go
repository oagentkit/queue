// 外部测试包（pglease_test）：同 pglease_test.go 的理由——worker re-export
// pglease，包内测试再引 worker 会成环。
package pglease_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/oagentkit/queue/pglease"
)

// connErrStore 返回一个连不上的 store：校验在位则**触库之前**返回本包哨兵，
// 漏校验才报连接错误。用例不需要真库，且能区分「报错」与「报对了错」。
func connErrStore(t *testing.T) *pglease.PGLeaseStore {
	t.Helper()
	db, err := sql.Open("postgres", "host=127.0.0.1 port=1 user=x dbname=x sslmode=disable connect_timeout=1")
	if err != nil {
		t.Fatalf("sql.Open 只解析 DSN，不该失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return pglease.NewPGLeaseStore(db)
}

// TestOversizeIdentityRejectedBeforeDB 超长 key/holder 必须在触库之前被拒（queue#34）。
//
// 失效形态：`Migrate` 建的是 `key VARCHAR(64)` / `holder VARCHAR(128)`，但 Go 侧
// 不校验——超长值一路走到 Postgres，报的是
// `value too long for type character varying(64)`。调用方拿到的是 driver 通用
// 错误，**读不出哪个参数超限、上限是多少**：一个配置写错（key 拼了长前缀、
// holder 用了整条 URI）表现为「存储故障」，排查方向从一开始就是错的。
//
// TryAcquire 与 Release 两个入口都验：Release 的校验同样重要——空/超长 holder
// 的 DELETE 删不到自己那把锁，让位会静默失败。
func TestOversizeIdentityRejectedBeforeDB(t *testing.T) {
	s := connErrStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	longKey := strings.Repeat("k", pglease.MaxKeyLen+1)
	longHolder := strings.Repeat("h", pglease.MaxHolderLen+1)

	t.Run("TryAcquire/key", func(t *testing.T) {
		ok, err := s.TryAcquire(ctx, longKey, "a", time.Minute)
		if !errors.Is(err, pglease.ErrKeyTooLong) {
			t.Fatalf("应返回 ErrKeyTooLong，得到 ok=%v err=%v——走到 SQL 就是 DB 层通用错误", ok, err)
		}
		if ok {
			t.Fatal("超长 key 不得被判定为获取成功")
		}
	})

	t.Run("TryAcquire/holder", func(t *testing.T) {
		ok, err := s.TryAcquire(ctx, "lead", longHolder, time.Minute)
		if !errors.Is(err, pglease.ErrHolderTooLong) {
			t.Fatalf("应返回 ErrHolderTooLong，得到 ok=%v err=%v", ok, err)
		}
		if ok {
			t.Fatal("超长 holder 不得被判定为获取成功")
		}
	})

	t.Run("Release/key", func(t *testing.T) {
		err := s.Release(ctx, longKey, "a")
		if !errors.Is(err, pglease.ErrKeyTooLong) {
			t.Fatalf("Release 应返回 ErrKeyTooLong，得到 %v", err)
		}
	})

	t.Run("Release/holder", func(t *testing.T) {
		err := s.Release(ctx, "lead", longHolder)
		if !errors.Is(err, pglease.ErrHolderTooLong) {
			t.Fatalf("Release 应返回 ErrHolderTooLong，得到 %v", err)
		}
	})
}

// TestLengthGuardCountsRunesNotBytes 列宽按**字符**计，判据必须同口径。
//
// PostgreSQL 的 `VARCHAR(64)` 是 64 个字符，不是 64 字节。逐字节判会把
// 64 个汉字（64 字符 / 192 字节）判成超限——那是把**合法输入拦在本地**，
// 比不校验更难查：调用方看到「key 超长」却发现自己的 key 明明只有 64 个字。
// 本用例就是这条选择的判别点：字节口径下第一条子用例会红。
func TestLengthGuardCountsRunesNotBytes(t *testing.T) {
	s := connErrStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// 64 个汉字 = 64 字符 / 192 字节：合法，必须放行到下一步（连接错误）。
	cn64 := strings.Repeat("键", pglease.MaxKeyLen)
	_, err := s.TryAcquire(ctx, cn64, "a", time.Minute)
	if errors.Is(err, pglease.ErrKeyTooLong) {
		t.Fatalf("%d 个汉字（%d 字节）是 %d 字符，不该被当成超长: %v",
			pglease.MaxKeyLen, len(cn64), pglease.MaxKeyLen, err)
	}
	if err == nil {
		t.Fatal("连不上的 DSN 不该成功——用例前提失效，请检查网络环境")
	}

	// 65 个汉字：超限。
	cn65 := strings.Repeat("键", pglease.MaxKeyLen+1)
	if _, err := s.TryAcquire(ctx, cn65, "a", time.Minute); !errors.Is(err, pglease.ErrKeyTooLong) {
		t.Fatalf("%d 个汉字应判超长，得到 %v", pglease.MaxKeyLen+1, err)
	}
}

// TestBoundaryLengthsPassGuard 恰好等于上限的值不得被拒（成对对照）：
// 否则「一律拒绝」也能让上一条绿，而按上限配的合法 key 全部不可用。
func TestBoundaryLengthsPassGuard(t *testing.T) {
	s := connErrStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := s.TryAcquire(ctx, strings.Repeat("k", pglease.MaxKeyLen),
		strings.Repeat("h", pglease.MaxHolderLen), time.Minute)
	if errors.Is(err, pglease.ErrKeyTooLong) || errors.Is(err, pglease.ErrHolderTooLong) {
		t.Fatalf("恰好在列宽上限上的 key/holder 不该被拒: %v", err)
	}
	if err == nil {
		t.Fatal("连不上的 DSN 不该成功——用例前提失效，请检查网络环境")
	}
}
