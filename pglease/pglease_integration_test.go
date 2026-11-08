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

// TestIntegrationCleanupRemovesExpiredOnly 过期清理只删过期行（queue#33）。
//
// stub driver 那两条用例只能证「语句形态与返回值透传」——「过期行真的被删、
// 活着的行真的留下」只有真库能证。这里造一行过期（ttl=1ms 后等过）与一行
// 活着（ttl=1h），断言 Cleanup 返回 1、活行仍在（本人可续约）。
func TestIntegrationCleanupRemovesExpiredOnly(t *testing.T) {
	s := integrationStore(t)
	ctx := context.Background()
	// 表是每用例重建的，但同表可能留了别的用例的行——先清空基线
	if _, err := s.Cleanup(ctx); err != nil {
		t.Fatalf("基线清理: %v", err)
	}
	if ok, _ := s.TryAcquire(ctx, "dead", "h1", time.Millisecond); !ok {
		t.Fatal("前置：dead 获取")
	}
	if ok, _ := s.TryAcquire(ctx, "alive", "h2", time.Hour); !ok {
		t.Fatal("前置：alive 获取")
	}
	time.Sleep(30 * time.Millisecond) // 让 dead 过期（DB 时钟）
	n, err := s.Cleanup(ctx)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if n != 1 {
		t.Fatalf("应只删掉 1 行过期租约，实得 %d——多删即删活租约（独占性失守）", n)
	}
	// 活行仍在：本人可续约
	if ok, _ := s.TryAcquire(ctx, "alive", "h2", time.Hour); !ok {
		t.Fatal("活着的租约被清理掉了——Cleanup 的判据方向错了")
	}
	// 过期行已不在：他人可直接取得
	if ok, _ := s.TryAcquire(ctx, "dead", "h3", time.Hour); !ok {
		t.Fatal("过期行清理后应空闲可取得")
	}
}

// TestIntegrationFencingTokenMonotonic fencing token 的三个语义点只有真库能证
// （queue#39）：
//
//  1. 首次获取置 1；
//  2. **同一 holder 续约保持原值**——续约不该作废自己刚排队的写；
//  3. 易主（过期后他人抢到）+1，且**旧 token 的 CAS 从此写不进去**。
//
// 第 3 点是 fencing 存在的全部理由：它必须是数据库层拒绝，而不是
// 「调用方记得先查一下」。所以这里真的用旧 token 去 UPDATE 业务行，
// 断言影响行数为 0——而不是只比较 token 数字变大。
func TestIntegrationFencingTokenMonotonic(t *testing.T) {
	s := integrationStore(t)
	ctx := context.Background()
	db := s.db

	// 一张最小的「业务行」：`generation` 存**最后一次被接受的写的 token**。
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS agentkit_fence_biz (
		id BIGINT PRIMARY KEY, generation BIGINT NOT NULL, payload TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatalf("建业务表: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TABLE IF EXISTS agentkit_fence_biz") })

	// ① 首次获取 → 1
	ok, gen1, err := s.TryAcquireWithToken(ctx, "fk", "hA", time.Millisecond)
	if err != nil || !ok {
		t.Fatalf("首次获取: ok=%v err=%v", ok, err)
	}
	if gen1 != 1 {
		t.Fatalf("首次 token = %d, want 1", gen1)
	}

	// 业务行以持有者当前 token 为起点。
	if _, err := db.ExecContext(ctx,
		`INSERT INTO agentkit_fence_biz (id, generation) VALUES (1, $1)
		 ON CONFLICT (id) DO UPDATE SET generation = $1, payload = ''`, gen1); err != nil {
		t.Fatalf("重置业务行: %v", err)
	}

	// 对照（-control）：持权者用**当前** token 可以反复写——这条必须先绿，
	// 否则下面「写不进去」的断言可能只是因为 CAS 形态本身就不成立。
	if n := casWrite(t, db, gen1); n != 1 {
		t.Fatalf("持权者用当前 token 写应有 1 行受影响，实得 %d", n)
	}

	// ② 同一 holder 续约 → 保持原值（续约不该作废自己刚排队的写）。
	// 续约用同样的短 ttl：下一步要让它过期，否则易主根本不会发生
	// （token 语义与 ttl 无关，短 ttl 不影响这条断言）。
	ok, genAgain, err := s.TryAcquireWithToken(ctx, "fk", "hA", time.Millisecond)
	if err != nil || !ok {
		t.Fatalf("本人续约: ok=%v err=%v", ok, err)
	}
	if genAgain != gen1 {
		t.Fatalf("本人续约后 token %d → %d——续约会作废自己刚排队的写", gen1, genAgain)
	}

	// ③ 过期后易主 → +1
	time.Sleep(30 * time.Millisecond) // 让 ttl=1ms 的租约过期（DB 时钟）
	ok, gen2, err := s.TryAcquireWithToken(ctx, "fk", "hB", time.Hour)
	if err != nil || !ok {
		t.Fatalf("过期后他人夺取: ok=%v err=%v", ok, err)
	}
	if gen2 != gen1+1 {
		t.Fatalf("易主后 token = %d, want %d（旧值+1）", gen2, gen1+1)
	}

	// 新主接管后先写一次（真实场景里的「接管动作」）。
	if n := casWrite(t, db, gen2); n != 1 {
		t.Fatalf("新 token %d 应写得进去，实得 %d 行", gen2, n)
	}
	// 处理（-treatment）：同一个调用、同一个旧 token，现在必须被数据库拒绝。
	// 与上面的对照成对——只看「0 行」无法区分「fencing 生效」与「CAS 写错了」。
	if n := casWrite(t, db, gen1); n != 0 {
		t.Fatalf("旧 token %d 仍然写得进去（%d 行）——fencing 在库层没生效", gen1, n)
	}
}

// casWrite 模拟「动作落库时带 fencing 校验」，返回受影响行数。
//
// 形态取 `generation <= $token`（行上存「最后一次被接受的写的 token」）：
// 同一持有者可反复写、严格更旧的 token 恒被拒；代价是「新主接管后的首写之前」
// 旧主的写仍可能落地——那一段由「接管后立即写一次」关闭（见用例注释）。
func casWrite(t *testing.T, db *sql.DB, token int64) int64 {
	t.Helper()
	res, err := db.ExecContext(context.Background(),
		`UPDATE agentkit_fence_biz SET generation = $1, payload = 'written' WHERE id = 1 AND generation <= $2`,
		token, token)
	if err != nil {
		t.Fatalf("CAS 写: %v", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("RowsAffected: %v", err)
	}
	return n
}

// TestIntegrationFencingTokenSurvivesReleaseAndCleanup fencing token 必须**跨
// 租约行生命周期**存续（queue#89，真 PostgreSQL 实弹复现的修复回归）：
//
// 修复前的确定性故障：任期1 hA token=1 写业务行 → 1；过期易主 hB token=2 写 → 2；
// hB 优雅停机（Release 删行）→ hC 合法继任得 **token=1**，对业务行（generation=2）
// 的 fenced 写 2<=1 恒 0 行——合法新主被数据库永久拒绝，而 stale 旧 token 反而是
// 最高有效 token（方向性反转）。Cleanup 路径同款复现。
//
// 修后：fenced 行 Release 置墓碑（generation 保留）、Cleanup 免删——三个任期
// 的 token 单调递增，旧 token 恒被拒。
func TestIntegrationFencingTokenSurvivesReleaseAndCleanup(t *testing.T) {
	s := integrationStore(t)
	ctx := context.Background()
	db := s.db

	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS agentkit_fence_biz89 (
		id BIGINT PRIMARY KEY, generation BIGINT NOT NULL, payload TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatalf("建业务表: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TABLE IF EXISTS agentkit_fence_biz89") })
	casWrite89 := func(token int64) int64 {
		t.Helper()
		res, err := db.ExecContext(ctx,
			`UPDATE agentkit_fence_biz89 SET generation = $1, payload = 'written' WHERE id = 1 AND generation <= $2`,
			token, token)
		if err != nil {
			t.Fatalf("CAS 写: %v", err)
		}
		n, _ := res.RowsAffected()
		return n
	}

	// 任期 1：hA token=1，业务行推到 1
	ok, gen1, err := s.TryAcquireWithToken(ctx, "fk89", "hA", time.Millisecond)
	if err != nil || !ok || gen1 != 1 {
		t.Fatalf("任期1: ok=%v gen=%d err=%v", ok, gen1, err)
	}
	// 业务行以持有者当前 token 为起点（与 TestIntegrationFencingTokenMonotonic 同款播种）
	if _, err := db.ExecContext(ctx,
		`INSERT INTO agentkit_fence_biz89 (id, generation) VALUES (1, $1)
		 ON CONFLICT (id) DO UPDATE SET generation = $1, payload = ''`, gen1); err != nil {
		t.Fatalf("播种业务行: %v", err)
	}
	if n := casWrite89(gen1); n != 1 {
		t.Fatalf("任期1 写应成功，实得 %d 行", n)
	}

	// hB 过期易主 → token=2（修复前该步已单调）
	time.Sleep(20 * time.Millisecond)
	ok, gen2, err := s.TryAcquireWithToken(ctx, "fk89", "hB", time.Hour)
	if err != nil || !ok {
		t.Fatalf("任期2 夺取: ok=%v err=%v", ok, err)
	}
	if gen2 != 2 {
		t.Fatalf("任期2 token = %d, want 2", gen2)
	}
	if n := casWrite89(gen2); n != 1 {
		t.Fatalf("任期2 写应成功，实得 %d 行", n)
	}

	// 优雅停机让位（LeaderElector.yield 的路径）——fenced 行必须墓碑化而非删除
	if err := s.Release(ctx, "fk89", "hB"); err != nil {
		t.Fatalf("Release: %v", err)
	}

	// hC 合法继任 → token 必须是 3 而不是复位成 1（#89 的核心断言）。
	// ttl 取短：本用例末段要让任期 3 过期后再验 Cleanup 免删。
	ok, gen3, err := s.TryAcquireWithToken(ctx, "fk89", "hC", time.Millisecond)
	if err != nil || !ok {
		t.Fatalf("任期3 继任: ok=%v err=%v", ok, err)
	}
	if gen3 != 3 {
		t.Fatalf("任期3 token = %d, want 3——Release 删行会把 token 复位，旧主反而成最高有效 token（#89）", gen3)
	}
	if n := casWrite89(gen3); n != 1 {
		t.Fatalf("任期3 首写应成功（新主不被自己拒），实得 %d 行", n)
	}
	// stale 旧主（hA，token=1）此刻写必须被库层拒绝
	if n := casWrite89(gen1); n != 0 {
		t.Fatalf("stale token %d 写得进去（%d 行）——fencing 方向性反转（#89）", gen1, n)
	}

	// Cleanup 免删 fenced 行：让任期 3 的租约过期再清——fenced 行必须在
	time.Sleep(20 * time.Millisecond)
	n, err := s.Cleanup(ctx)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if n != 0 {
		t.Fatalf("Cleanup 不得删除 fenced 行（实得 %d 行）——删掉后新 token 从 1 起步（#89）", n)
	}
	// 过期 fenced 行照常可夺（独占性不受「不清理」影响），token 继续单调
	ok, gen4, err := s.TryAcquireWithToken(ctx, "fk89", "hD", time.Hour)
	if err != nil || !ok {
		t.Fatalf("过期 fenced 行应可夺取: ok=%v err=%v", ok, err)
	}
	if gen4 != 4 {
		t.Fatalf("任期4 token = %d, want 4", gen4)
	}
}

// TestIntegrationElectorTakeoverBumpsGeneration elector 路径（TryAcquire——
// LeaderElector.tick 唯一使用的路径）易主必须推高 generation（queue#90）：
//
// 修复前的确定性故障：hA token=1 写业务行 → 1，租约过期；副本 B 的
// LeaderElector（LeaseStore 路径）夺走租约，generation 仍=1——hA 的在飞
// fenced 写 1<=1 照写成功（恰发生在 fencing 存在的全部理由那一刻）；随后 hB
// 用 TryAcquireWithToken 拿到的 token 与旧主完全相同，库层无从区分新旧主。
func TestIntegrationElectorTakeoverBumpsGeneration(t *testing.T) {
	s := integrationStore(t)
	ctx := context.Background()
	db := s.db

	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS agentkit_fence_biz90 (
		id BIGINT PRIMARY KEY, generation BIGINT NOT NULL, payload TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatalf("建业务表: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TABLE IF EXISTS agentkit_fence_biz90") })
	casWrite90 := func(token int64) int64 {
		t.Helper()
		res, err := db.ExecContext(ctx,
			`UPDATE agentkit_fence_biz90 SET generation = $1, payload = 'written' WHERE id = 1 AND generation <= $2`,
			token, token)
		if err != nil {
			t.Fatalf("CAS 写: %v", err)
		}
		n, _ := res.RowsAffected()
		return n
	}

	// hA：token 路径获取（关键写）→ token=1，业务行推到 1
	ok, gen1, err := s.TryAcquireWithToken(ctx, "fk90", "hA", time.Millisecond)
	if err != nil || !ok || gen1 != 1 {
		t.Fatalf("前置: ok=%v gen=%d err=%v", ok, gen1, err)
	}
	// 业务行以持有者当前 token 为起点（同款播种）
	if _, err := db.ExecContext(ctx,
		`INSERT INTO agentkit_fence_biz90 (id, generation) VALUES (1, $1)
		 ON CONFLICT (id) DO UPDATE SET generation = $1, payload = ''`, gen1); err != nil {
		t.Fatalf("播种业务行: %v", err)
	}
	if n := casWrite90(gen1); n != 1 {
		t.Fatalf("hA 首写应成功，实得 %d 行", n)
	}

	// 租约过期；副本 B 的 LeaderElector 走 **TryAcquire**（非 token 路径）夺约
	time.Sleep(20 * time.Millisecond)
	if ok, err := s.TryAcquire(ctx, "fk90", "hB", time.Hour); err != nil || !ok {
		t.Fatalf("elector 路径夺取: ok=%v err=%v", ok, err)
	}

	// hB 关键写取 token：同 holder 续约保持 elector 易主时推高的值——
	// 必须是 2 而不是 1（与旧主同 token = 库层无从区分新旧主、原子拒绝
	// 无从发生）。这是 #90 的核心断言：elector 易主没推高 generation 的话
	// 这里拿到的 token 与旧主完全相同。
	ok, gen2, err := s.TryAcquireWithToken(ctx, "fk90", "hB", time.Hour)
	if err != nil || !ok {
		t.Fatalf("hB 取 token: ok=%v err=%v", ok, err)
	}
	if gen2 != 2 {
		t.Fatalf("hB token = %d, want 2——与旧主同 token 则原子拒绝无从发生（#90）", gen2)
	}

	// 新主接管后先写一次（包文档成对纪律：关闭「接管后首写之前」的 `<=`
	// 交叠窗口——那一段是 CAS 判据的文档化代价，不是本 issue 的缺陷面）。
	if n := casWrite90(gen2); n != 1 {
		t.Fatalf("hB 用 token %d 应写得进去，实得 %d 行", gen2, n)
	}
	// 业务行已被推到 2：hA 的在飞 stale 写（token=1）从此恒被库层拒绝。
	if n := casWrite90(gen1); n != 0 {
		t.Fatalf("stale token %d 仍写得进去（%d 行）——elector 路径易主没推高 generation（#90）", gen1, n)
	}
}
