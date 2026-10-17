package pglease

// DSN 门控集成测试：AGENTKIT_PG_TEST_DSN 非空时跑真 PostgreSQL 五语义点
//（空闲获取/本人续约/他人持有拒/过期可夺/非持有者 Release no-op）。
// 空时 t.Skip——仓内 go test 不依赖外部环境（第九/十轮挂账落地）。
//
// 用法：AGENTKIT_PG_TEST_DSN="postgres://user:pass@localhost:5432/db?sslmode=disable" go test ./worker/pglease/ -run Integration

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq" // DSN 门控集成测试驱动（仓内零生产依赖；DSN 形态 postgres://…）
)

func integrationStore(t *testing.T) *PGLeaseStore {
	t.Helper()
	dsn := os.Getenv("AGENTKIT_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("AGENTKIT_PG_TEST_DSN 未设置，跳过真 PG 集成")
	}
	driver := os.Getenv("AGENTKIT_PG_TEST_DRIVER")
	if driver == "" {
		driver = "postgres"
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := NewPGLeaseStore(db).WithTable("agentkit_lease_itest")
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TABLE IF EXISTS agentkit_lease_itest") })
	return s
}

func TestIntegrationLeaseSemantics(t *testing.T) {
	s := integrationStore(t)
	ctx := context.Background()
	// 空闲获取
	if ok, _ := s.TryAcquire(ctx, "k1", "h1", 10*time.Second); !ok {
		t.Fatal("空闲 key 应获取成功")
	}
	// 本人续约
	if ok, _ := s.TryAcquire(ctx, "k1", "h1", 10*time.Second); !ok {
		t.Fatal("本人续约应成功")
	}
	// 他人持有未过期：拒
	if ok, _ := s.TryAcquire(ctx, "k1", "h2", 10*time.Second); ok {
		t.Fatal("他人持有未过期应被拒")
	}
	// 非持有者 Release：no-op（不报错）
	_ = s.Release(ctx, "k1", "h2")
	if ok, _ := s.TryAcquire(ctx, "k1", "h1", 10*time.Second); !ok {
		t.Fatal("非持有者 Release 后本人应仍持有")
	}
	// 过期后他人可夺（ttl=1ms）
	if ok, _ := s.TryAcquire(ctx, "k2", "hA", time.Millisecond); !ok {
		t.Fatal("前置：k2 获取")
	}
	time.Sleep(20 * time.Millisecond)
	if ok, _ := s.TryAcquire(ctx, "k2", "hB", 10*time.Second); !ok {
		t.Fatal("过期后他人应可夺")
	}
	// 本人 Release 后空闲
	_ = s.Release(ctx, "k1", "h1")
	if ok, _ := s.TryAcquire(ctx, "k1", "h3", 10*time.Second); !ok {
		t.Fatal("Release 后应空闲可获取")
	}
}
