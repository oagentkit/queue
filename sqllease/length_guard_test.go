package sqllease

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// 长度护栏（queue#83，pglease#34/#35 的三方言移植）。
//
// 失效形态：Migrate 建的是 key VARCHAR(64) / holder VARCHAR(128)，但 Go 侧
// 不校验——超长值一路走到 driver，三方言三种结局、全部静默或不可读：
//   - PostgreSQL：`value too long for type character varying(64)`——driver
//     通用错误，读不出哪个参数超限、上限多少（pglease/length_guard_test.go
//     头注释逐字引用过的那条）；
//   - MySQL strict：同上（Data too long）；非 strict 更糟：**静默截断**到
//     64 字符，前 64 字符相同的两个 key 塌成同一把锁（假互斥/假独占）；
//   - SQLite：完全放行（忽略 VARCHAR 宽度），超长 key 原样落库。
//
// 判据（pglease 同款）：哨兵错误 + driver 零调用 + 三方言各跑一遍 + 边界放行
// + rune 口径。
func TestOversizeIdentityRejectedBeforeDB(t *testing.T) {
	for _, dialect := range allDialects() {
		t.Run(dialectName(dialect), func(t *testing.T) {
			s, d := newStubStore(t, dialect, 1)
			ctx, cancel := guardDeadline()
			defer cancel()

			longKey := strings.Repeat("k", MaxKeyLen+1)
			longHolder := strings.Repeat("h", MaxHolderLen+1)

			t.Run("TryAcquire/key", func(t *testing.T) {
				ok, err := s.TryAcquire(ctx, longKey, "a", time.Minute)
				if !errors.Is(err, ErrKeyTooLong) {
					t.Fatalf("应返回 ErrKeyTooLong，得到 ok=%v err=%v——走到 SQL 就是 DB 层通用错误或静默截断", ok, err)
				}
				if ok {
					t.Fatal("超长 key 不得被判定为获取成功")
				}
			})

			t.Run("TryAcquire/holder", func(t *testing.T) {
				ok, err := s.TryAcquire(ctx, "lead", longHolder, time.Minute)
				if !errors.Is(err, ErrHolderTooLong) {
					t.Fatalf("应返回 ErrHolderTooLong，得到 ok=%v err=%v", ok, err)
				}
				if ok {
					t.Fatal("超长 holder 不得被判定为获取成功")
				}
			})

			t.Run("Release/key", func(t *testing.T) {
				// Release 的校验同样重要：超长 holder 的 DELETE 删不到自己那把
				// 锁，让位会静默失败（pglease/length_guard_test.go 已钉住这条）。
				if err := s.Release(ctx, longKey, "a"); !errors.Is(err, ErrKeyTooLong) {
					t.Fatalf("Release 应返回 ErrKeyTooLong，得到 %v", err)
				}
			})

			t.Run("Release/holder", func(t *testing.T) {
				if err := s.Release(ctx, "lead", longHolder); !errors.Is(err, ErrHolderTooLong) {
					t.Fatalf("Release 应返回 ErrHolderTooLong，得到 %v", err)
				}
			})

			// driver 零调用：校验必须发生在触库之前。只看「返回了错误」证不出
			// 这一点——那条判据在「先触库、拿到 driver 错误再返回」的错实现下
			// 照样绿。
			if got := d.taken(); len(got) != 0 {
				t.Fatalf("超长身份必须被拦在触库之前（driver 零调用），实得 %d 条语句: %v", len(got), got)
			}
		})
	}
}

// TestLengthGuardCountsRunesNotBytes 列宽按**字符**计，判据必须同口径。
//
// SQL 的 VARCHAR(64) 是 64 个字符，不是 64 字节。逐字节判会把 64 个汉字
// （64 字符 / 192 字节）判成超限——那是把合法输入拦在本地，比不校验更难查。
// 本用例就是这条选择的判别点：字节口径下第一条子用例会红。
func TestLengthGuardCountsRunesNotBytes(t *testing.T) {
	for _, dialect := range allDialects() {
		t.Run(dialectName(dialect), func(t *testing.T) {
			s, d := newStubStore(t, dialect, 1)
			ctx, cancel := guardDeadline()
			defer cancel()

			// 64 个汉字 = 64 字符 / 192 字节：合法，必须放行并真正触达 driver。
			cn64 := strings.Repeat("键", MaxKeyLen)
			ok, err := s.TryAcquire(ctx, cn64, "a", time.Minute)
			if errors.Is(err, ErrKeyTooLong) {
				t.Fatalf("%d 个汉字（%d 字节）是 %d 字符，不该被当成超长: %v",
					MaxKeyLen, len(cn64), MaxKeyLen, err)
			}
			if err != nil || !ok {
				t.Fatalf("rune 口径下 64 汉字 key 应放行且获取成功: ok=%v err=%v", ok, err)
			}
			if len(d.taken()) == 0 {
				t.Fatal("合法长度的 key 应触达 driver——guard 只拦超长，不得误拒")
			}

			// 65 个汉字：超限。
			cn65 := strings.Repeat("键", MaxKeyLen+1)
			if _, err := s.TryAcquire(ctx, cn65, "a", time.Minute); !errors.Is(err, ErrKeyTooLong) {
				t.Fatalf("%d 个汉字应判超长，得到 %v", MaxKeyLen+1, err)
			}
		})
	}
}

// TestBoundaryLengthsPassGuard 恰好等于上限的值不得被拒（成对对照）：
// 否则「一律拒绝」也能让上一条绿，而按上限配的合法 key 全部不可用。
func TestBoundaryLengthsPassGuard(t *testing.T) {
	for _, dialect := range allDialects() {
		t.Run(dialectName(dialect), func(t *testing.T) {
			s, d := newStubStore(t, dialect, 1)
			ctx, cancel := guardDeadline()
			defer cancel()

			ok, err := s.TryAcquire(ctx,
				strings.Repeat("k", MaxKeyLen),
				strings.Repeat("h", MaxHolderLen), time.Minute)
			if errors.Is(err, ErrKeyTooLong) || errors.Is(err, ErrHolderTooLong) {
				t.Fatalf("恰好在列宽上限上的 key/holder 不该被拒: %v", err)
			}
			if err != nil || !ok {
				t.Fatalf("边界长度的身份应正常获取: ok=%v err=%v", ok, err)
			}
			if len(d.taken()) == 0 {
				t.Fatal("边界长度应触达 driver")
			}
		})
	}
}

// TestMigrateColumnWidthsDerivedFromConstants DDL 列宽必须由 MaxKeyLen/
// MaxHolderLen 派生（queue#83 顺带判据）：护栏与 DDL 同源——两处各写一个
// 数字、改一处忘另一处，本缺陷就会复现。本用例在常量被改动而 DDL 仍写死
// 旧值时会红。
func TestMigrateColumnWidthsDerivedFromConstants(t *testing.T) {
	for _, dialect := range allDialects() {
		t.Run(dialectName(dialect), func(t *testing.T) {
			s, d := newStubStore(t, dialect, 0)
			if err := s.Migrate(context.Background()); err != nil {
				t.Fatalf("Migrate: %v", err)
			}
			stmts := d.taken()
			if len(stmts) != 1 {
				t.Fatalf("应只有一条建表语句: %v", stmts)
			}
			ddl := stmts[0].query
			for _, want := range []string{
				fmt.Sprintf("VARCHAR(%d)", MaxKeyLen),
				fmt.Sprintf("VARCHAR(%d)", MaxHolderLen),
			} {
				if !strings.Contains(ddl, want) {
					t.Fatalf("DDL 列宽应由常量派生（缺失 %s）:\n%s", want, ddl)
				}
			}
		})
	}
}
