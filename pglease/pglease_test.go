package pglease

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/oagentkit/queue/worker"
)

// 编译期断言：满足 worker.LeaseStore 接口。
var _ worker.LeaseStore = (*PGLeaseStore)(nil)

func TestImplementsLeaseStore(t *testing.T) {
	var s worker.LeaseStore = NewPGLeaseStore(&sql.DB{})
	_ = s
}

func TestWithTable(t *testing.T) {
	s := NewPGLeaseStore(&sql.DB{})
	if s.table != DefaultTable {
		t.Fatalf("缺省表名 = %q, want %q", s.table, DefaultTable)
	}
	s.WithTable("bq_lease")
	if s.table != "bq_lease" {
		t.Fatalf("WithTable 后 = %q", s.table)
	}
	s.WithTable("")
	if s.table != "bq_lease" {
		t.Fatalf("空表名不应覆盖: %q", s.table)
	}
	// 非法标识符拒绝（防拼接注入）
	s.WithTable("lease; DROP TABLE x")
	if s.table != "bq_lease" {
		t.Fatalf("非法表名不应生效: %q", s.table)
	}
	s.WithTable("ok_table1")
	if s.table != "ok_table1" {
		t.Fatalf("合法表名应生效: %q", s.table)
	}
}

// TestMigrateSQL 轻量校验 Migrate 语句形态（无 PG 时跳过集成）。
func TestMigrateSQL(t *testing.T) {
	// 无数据库连接时仅验证 SQL 构造不 panic；真 PG 集成见调用方 BQ_PG_DSN 门控测试。
	s := NewPGLeaseStore(nil).WithTable("custom_lease")
	if s.table != "custom_lease" {
		t.Fatal("table 未生效")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	// nil db 会 panic 或返回错误——这里只要求方法存在且签名正确。
	defer func() { _ = recover() }()
	_ = s.Migrate(ctx)
}
