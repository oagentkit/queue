// 外部测试包（pglease_test）：worker 现 re-export pglease（lease_ref.go），
// worker → pglease 的 import 若在包内测试里再引 worker 会成环——接口断言
// 放外部测试包，两侧互不可见地通过。
package pglease_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	_ "github.com/lib/pq" // 只为注册 "postgres" 驱动名：下面的用例给的是连不上的 DSN

	"github.com/oagentkit/queue/pglease"
	"github.com/oagentkit/queue/worker"
)

// 编译期断言：满足 worker.LeaseStore 接口。
var _ worker.LeaseStore = (*pglease.PGLeaseStore)(nil)

func TestImplementsLeaseStore(t *testing.T) {
	var s worker.LeaseStore = pglease.NewPGLeaseStore(&sql.DB{})
	_ = s
}

// TestEmptyIdentityRejectedBeforeDB 空 key / 空 holder 必须在**触库之前**被拒
// （queue#43）：冲突解决子句的 `holder = EXCLUDED.holder` 是内容比对，空串对
// 它恒真 ⇒ 所有空 holder 副本互相认作本人 ⇒ 双主且零报错。
//
// 这里刻意给一个**连不上**的 DSN：若校验漏掉，错误会是连接错误（而不是本包的
// 哨兵错误）⇒ 断言红；校验在位则在建立连接之前就返回。这样用例既不需要真库，
// 又能区分「报错」与「报对了错」——只断言 err != nil 的用例对这两种情形都绿。
func TestEmptyIdentityRejectedBeforeDB(t *testing.T) {
	db, err := sql.Open("postgres", "host=127.0.0.1 port=1 user=x dbname=x sslmode=disable connect_timeout=1")
	if err != nil {
		t.Fatalf("sql.Open 只解析 DSN，不该失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := pglease.NewPGLeaseStore(db)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ok, err := s.TryAcquire(ctx, "lead", "", time.Minute)
	if !errors.Is(err, pglease.ErrEmptyHolder) {
		t.Fatalf("空 holder 应返回 ErrEmptyHolder，得到 ok=%v err=%v——走到 SQL 即双主", ok, err)
	}
	if ok {
		t.Fatal("空 holder 不得被判定为获取成功（双主）")
	}
	if _, err := s.TryAcquire(ctx, "", "a", time.Minute); !errors.Is(err, pglease.ErrEmptyKey) {
		t.Fatalf("空 key 应返回 ErrEmptyKey，得到 %v", err)
	}
	if err := s.Release(ctx, "lead", ""); !errors.Is(err, pglease.ErrEmptyHolder) {
		t.Fatalf("空 holder 的 Release 应返回 ErrEmptyHolder，得到 %v", err)
	}
	if err := s.Release(ctx, "", "a"); !errors.Is(err, pglease.ErrEmptyKey) {
		t.Fatalf("空 key 的 Release 应返回 ErrEmptyKey，得到 %v", err)
	}
}
