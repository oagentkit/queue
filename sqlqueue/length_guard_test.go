package sqlqueue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// 长度护栏（queue#85，pglease#34 / sqllease#83 同族、不同位置）。
//
// 失效形态：Migrate 声明 task_id VARCHAR(64) / runner_id VARCHAR(128)，Go 侧
// 却把任意长度的值原样交给 driver——PG/MySQL strict 报 driver 通用错误
// （读不出哪个参数超限、上限多少；runnerID 那条报错点在**首次 claim** 的后台
// 守护循环，装配期反馈彻底丢失）；MySQL 非 strict **静默截断**（两个语义不同
// 的任务塌成同一条主键；两个只差后缀的副本互认同一身份，心跳互相续命）；
// SQLite 完全放行。
//
// 判据：构造期 panic（runnerID）/ 哨兵错误（taskID）+ driver 零调用 + 三方言
// 各跑一遍 + 边界放行 + rune 口径。
func TestOversizeRunnerIDPanicsAtConstruction(t *testing.T) {
	for _, dialect := range sqAllDialects() {
		t.Run(sqDialectName(dialect), func(t *testing.T) {
			db, d := newMigStubDB(t, dialect, 0)
			func() {
				defer func() {
					r := recover()
					if r == nil {
						t.Fatal("超长 runnerID 应构造期 panic——推迟到 DB 层则报错点在首次 claim 的后台循环，装配期反馈丢失")
					}
					msg := fmt.Sprint(r)
					if !strings.Contains(msg, "runnerID") || !strings.Contains(msg, "超长") {
						t.Fatalf("panic 应指向 runnerID 超长: %v", r)
					}
				}()
				_ = New(db, dialect, strings.Repeat("r", MaxRunnerIDLen+1))
			}()
			// 构造期闸门不得触库。
			if got := d.taken(); len(got) != 0 {
				t.Fatalf("超长 runnerID 必须被拦在触库之前，实得 %d 条语句: %v", len(got), got)
			}
		})
	}
}

// TestBoundaryRunnerIDAccepted 成对对照：恰好 128 字符的 runnerID 合法。
func TestBoundaryRunnerIDAccepted(t *testing.T) {
	for _, dialect := range sqAllDialects() {
		t.Run(sqDialectName(dialect), func(t *testing.T) {
			db, _ := newMigStubDB(t, dialect, 0)
			// 不 panic 即通过（New 无错误返回，闸门只有 panic 一档）。
			_ = New(db, dialect, strings.Repeat("r", MaxRunnerIDLen))
			// rune 口径：128 个汉字 = 128 字符 / 384 字节，同样放行。
			_ = New(db, dialect, strings.Repeat("副", MaxRunnerIDLen))
		})
	}
}

func TestOversizeTaskIDRejectedBeforeDB(t *testing.T) {
	for _, dialect := range sqAllDialects() {
		t.Run(sqDialectName(dialect), func(t *testing.T) {
			s, d := newMigStubStore(t, dialect, 0)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			long := strings.Repeat("t", MaxTaskIDLen+1)
			err := s.Enqueue(ctx, long)
			if !errors.Is(err, ErrTaskIDTooLong) {
				t.Fatalf("超长 taskID 应返回 ErrTaskIDTooLong，得到 %v", err)
			}
			// driver 零调用：只看「返回了错误」证不出校验在触库之前。
			if got := d.taken(); len(got) != 0 {
				t.Fatalf("超长 taskID 必须被拦在触库之前，实得 %d 条语句: %v", len(got), got)
			}

			// 边界放行：恰好 64 字符触达 driver。
			if err := s.Enqueue(ctx, strings.Repeat("t", MaxTaskIDLen)); err != nil {
				t.Fatalf("边界长度的 taskID 应放行: %v", err)
			}
			if got := d.taken(); len(got) != 1 {
				t.Fatalf("边界长度应触达 driver 一次，实得 %d 条语句", len(got))
			}

			// rune 口径：64 个汉字 = 64 字符 / 192 字节，合法。
			if err := s.Enqueue(ctx, strings.Repeat("务", MaxTaskIDLen)); err != nil {
				t.Fatalf("rune 口径下 %d 个汉字 taskID 应放行: %v", MaxTaskIDLen, err)
			}
			// 65 个汉字：超限。
			if err := s.Enqueue(ctx, strings.Repeat("务", MaxTaskIDLen+1)); !errors.Is(err, ErrTaskIDTooLong) {
				t.Fatalf("%d 个汉字 taskID 应判超长，得到 %v", MaxTaskIDLen+1, err)
			}
		})
	}
}

// TestLengthGuardRealSQLiteBoundaries 真 SQLite 的边界对照（stub 之外再钉一层：
// 三方言判据一致，SQLite 必须同样放行边界、拒绝超限——方言不得改变判据）。
func TestLengthGuardRealSQLiteBoundaries(t *testing.T) {
	ctx := context.Background()
	s := testStore(t, strings.Repeat("r", MaxRunnerIDLen)) // 边界 runnerID 不 panic
	if err := s.Enqueue(ctx, strings.Repeat("t", MaxTaskIDLen)); err != nil {
		t.Fatalf("边界 taskID 应入队成功: %v", err)
	}
	if err := s.Enqueue(ctx, strings.Repeat("务", MaxTaskIDLen)); err != nil {
		t.Fatalf("64 汉字 taskID 应入队成功（rune 口径）: %v", err)
	}
	if err := s.Enqueue(ctx, strings.Repeat("t", MaxTaskIDLen+1)); !errors.Is(err, ErrTaskIDTooLong) {
		t.Fatalf("超长 taskID 应返回哨兵，得到 %v", err)
	}
}

// TestMigrateColumnWidthsDerivedFromConstants DDL 列宽必须由 MaxTaskIDLen/
// MaxRunnerIDLen 派生（queue#85 顺带判据）：护栏与 DDL 同源——两处各写一个
// 数字、改一处忘另一处，本缺陷就会复现。
func TestMigrateColumnWidthsDerivedFromConstants(t *testing.T) {
	for _, dialect := range sqAllDialects() {
		t.Run(sqDialectName(dialect), func(t *testing.T) {
			s, d := newMigStubStore(t, dialect, 0)
			if err := s.Migrate(context.Background()); err != nil {
				t.Fatalf("Migrate: %v", err)
			}
			stmts := d.taken()
			if len(stmts) == 0 {
				t.Fatal("Migrate 应发出建表语句")
			}
			ddl := stmts[0].query
			for _, want := range []string{
				fmt.Sprintf("VARCHAR(%d)", MaxTaskIDLen),
				fmt.Sprintf("VARCHAR(%d)", MaxRunnerIDLen),
			} {
				if !strings.Contains(ddl, want) {
					t.Fatalf("DDL 列宽应由常量派生（缺失 %s）:\n%s", want, ddl)
				}
			}
		})
	}
}
