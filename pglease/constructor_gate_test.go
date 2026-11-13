package pglease_test

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	_ "github.com/lib/pq"

	"github.com/oagentkit/queue/pglease"
)

// 构造期 fail-fast（oagentkit/queue#69 的同仓纪律、#120 的继承缺口）：
// sqlqueue.New 与 worker.NewLeaderElector 都在构造期拦 nil db，#71 拆出的
// 本包此前没有——nil panic 被推迟到首次用语句的 goroutine，而本包的首触库点
// 在**常驻后台的租约轮询**里，装配期反馈彻底丢失。

func TestNewPGLeaseStoreRejectsNilDB(t *testing.T) {
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("nil db 应构造期 panic（此前静默接受，panic 推迟到租约轮询 goroutine 首次触库时）")
			}
			if !strings.Contains(fmt.Sprint(r), "db") {
				t.Fatalf("panic 应指向 db 为 nil: %v", r)
			}
		}()
		_ = pglease.NewPGLeaseStore(nil)
	}()
}

// 合法装配不被闸门误伤：非 nil db（只解析 DSN、不连库）构造与 WithTable 正常。
func TestNewPGLeaseStoreAcceptsValidAssembly(t *testing.T) {
	db, err := sql.Open("postgres", "host=127.0.0.1 port=1 user=x dbname=x sslmode=disable connect_timeout=1")
	if err != nil {
		t.Fatalf("sql.Open 只解析 DSN，不该失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	s := pglease.NewPGLeaseStore(db)
	if s == nil {
		t.Fatal("非 nil db 构造不应被闸门误伤")
	}
	if got := s.WithTable("custom_lease"); got != s {
		t.Fatal("WithTable 应返回同一实例（链式）")
	}
}
