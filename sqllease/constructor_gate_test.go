package sqllease_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动（测试依赖）

	"github.com/oagentkit/queue/sqllease"
)

// 构造期 fail-fast（oagentkit/queue#69 的同仓纪律、#120 的继承缺口）：
// sqlqueue.New 与 worker.NewLeaderElector 都在构造期拦 nil db，#71 拆出的
// 本包此前没有——nil panic 被推迟到首次用语句的 goroutine，而本包的首触库点
// 在**常驻后台的租约轮询**里，装配期反馈彻底丢失。

func TestNewRejectsNilDB(t *testing.T) {
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
		_ = sqllease.New(nil, sqllease.SQLite)
	}()
}

// 合法装配不被闸门误伤：Migrate 全链路可用（与 sqlqueue 闸门测试同款成对纪律）。
func TestNewAcceptsValidAssembly(t *testing.T) {
	db, err := sql.Open("sqlite", "file:gatevalid?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	s := sqllease.New(db, sqllease.SQLite)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
}
