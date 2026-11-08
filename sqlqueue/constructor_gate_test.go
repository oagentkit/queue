package sqlqueue_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动（测试依赖）

	"github.com/oagentkit/queue/sqlqueue"
)

// 构造期 fail-fast（oagentkit/queue#69）：New 此前对 nil db 与空 runnerID 一律
// 静默接受——nil db 在首次用语句的后台 goroutine 处 panic；空 runnerID 是副本身份
// 缺失（心跳归属 `WHERE runner_id = ?` 的键），多副本共用空身份会互相续命/互相
// 复位且零报错（queue#43 空身份家族、queue#42 静默丢失同型）。两者都必须在
// 构造期拦截，与 worker.NewLeaderElector 的参数闸门同一道纪律。

func TestNewRejectsNilDB(t *testing.T) {
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("nil db 应构造期 panic（此前静默接受，panic 推迟到首次用语句时）")
			}
			if !strings.Contains(fmt.Sprint(r), "db") {
				t.Fatalf("panic 应指向 db 为 nil: %v", r)
			}
		}()
		_ = sqlqueue.New(nil, sqlqueue.SQLite, "runner-a")
	}()
}

func TestNewRejectsEmptyRunnerID(t *testing.T) {
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("空 runnerID 应构造期 panic（此前静默接受，行以 runner_id='' 入库且无任何报错）")
			}
			if !strings.Contains(fmt.Sprint(r), "runnerID") {
				t.Fatalf("panic 应指向 runnerID 为空: %v", r)
			}
		}()
		_ = sqlqueue.New(sqliteMemDB(t), sqlqueue.SQLite, "")
	}()
}

// 合法装配不被闸门误伤：Migrate → Enqueue → Claim 全链路可用，行归属正确。
func TestNewAcceptsValidAssembly(t *testing.T) {
	ctx := context.Background()
	s := sqlqueue.New(sqliteMemDB(t), sqlqueue.SQLite, "runner-a")
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Enqueue(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	id, ok, err := s.ClaimNextPending(ctx)
	if err != nil || !ok || id != "t1" {
		t.Fatalf("claim = %q ok=%v err=%v, want t1", id, ok, err)
	}
}

func sqliteMemDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:memdb?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1) // :memory: 每连接独立库——共享缓存 + 单连接
	t.Cleanup(func() { _ = db.Close() })
	return db
}
