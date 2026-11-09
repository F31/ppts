//go:build pg

package pipeline

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/retention"
	"github.com/F31/ppts/internal/tenant"
)

// growthCandidateQuery 是 ppts_claim_next_job 候选 CTE 的原样抽取（与 explainGrowthCandidate
// 同形）。用于直接量扫描成本，绕过函数的 PL/pgSQL / SECURITY DEFINER 额外开销，隔离「表只增不减」
// 这一项的账单——保留策略要消除的正是它。
const growthCandidateQuery = `
WITH tenant_load AS (
    SELECT tenant_id, count(*) AS running_count,
           count(*) FILTER (WHERE kind='narration') AS running_narration
    FROM jobs WHERE state IN ('running','cancel_requested') GROUP BY tenant_id
), tenant_cap AS (
    SELECT id, COALESCE(NULLIF((policy->>'max_concurrent_jobs')::int, 0), 2147483647) AS cap
    FROM tenants
), candidate AS (
    SELECT j.id FROM jobs j
    LEFT JOIN tenant_load l ON l.tenant_id = j.tenant_id
    LEFT JOIN tenant_cap c ON c.id = j.tenant_id
    WHERE (j.run_at IS NULL OR j.run_at <= now())
      AND (
        (j.state IN ('queued','retry_wait') AND (j.lease_until IS NULL OR j.lease_until < now()))
        OR (j.state IN ('running','cancel_requested') AND j.lease_until < now())
      )
      AND (j.kind <> 'narration' OR (COALESCE(l.running_narration, 0) < COALESCE(c.cap, 2147483647)))
    ORDER BY coalesce(l.running_count, 0), j.created_at
    LIMIT 1
    FOR UPDATE OF j SKIP LOCKED
)
SELECT * FROM candidate`

// TestPGJobTableGrowthCleanup 量化保留清理对「表只增不减」的反制效果（P3-6 端到端证据）：
// 把一个租户灌到 16 万终态任务（与基线工具终态分布一致），量 claim-idle（预期 ~30ms，
// 这是调度器空转心跳、无论有没有活都在付），再跑 retention 的 DeleteExpiredJobs 把终态行
// 清掉，重量 claim-idle（预期回落到基线 ~570µs）。
//
// 证明两件事：(1) claim-idle 随终态行线性涨、随清理回落——保留策略确实把「增长」这一项
// 从账单里划掉了；(2) 清理本身的成本结构：首批慢（全租户 seq scan 找 500 个终态行）、
// 末批快（近空表）——这正是迁移 0055 要解决的（本机测试库未应用 0055，需要 superuser
// 建索引；0055 把 seq scan 换成部分索引扫描，详见 docs/优化方案 的 P3-6 段论证）。
//
// 默认跳过：设 PPTS_GROWTH_CLEANUP=1 与 PPTS_TEST_DATABASE 才执行（一次灌 16 万任务 +
// 64 万事件、约 1GB）。与基线工具 TestPGJobTableGrowthBaseline 用不同的门控，避免叠加成本。
// 跑法：PPTS_GROWTH_CLEANUP=1 PPTS_TEST_DATABASE=... go test -tags=pg ./internal/pipeline/ -run TestPGJobTableGrowthCleanup -v -timeout 30m
func TestPGJobTableGrowthCleanup(t *testing.T) {
	if os.Getenv("PPTS_GROWTH_CLEANUP") == "" {
		t.Skip("set PPTS_GROWTH_CLEANUP=1 to run the growth cleanup demo (seeds 160k jobs / 640k events, ~1GB)")
	}
	dsn := os.Getenv("PPTS_TEST_DATABASE")
	if dsn == "" {
		t.Skip("PPTS_TEST_DATABASE not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	tenantID := uuid.NewString()
	projectID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES($1,'growth-cleanup')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if err := tenant.Run(ctx, pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO projects(id,tenant_id,owner_user,title) VALUES($1,$2,'tester','growth-cleanup')`,
			projectID, tenantID)
		return err
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	t.Cleanup(func() {
		if err := tenant.Run(ctx, pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
			for _, sql := range []string{
				`DELETE FROM job_events WHERE tenant_id=$1`,
				`DELETE FROM jobs WHERE tenant_id=$1`,
				`DELETE FROM projects WHERE tenant_id=$1`,
			} {
				if _, err := tx.Exec(ctx, sql, tenantID); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Logf("cleanup tenant rows: %v", err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM tenants WHERE id=$1`, tenantID); err != nil {
			t.Logf("cleanup tenant: %v", err)
		}
	})

	// 常驻可领取目标：91 天前入队，claim-idle 时不可领取（run_at 设到未来），claim-hit 时复位。
	// 它是 queued（非终态），保留清理不会碰它——清完积压后它还在、claim 仍能领到。
	targetID := uuid.NewString()
	if err := tenant.Run(ctx, pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO jobs(id,tenant_id,project_id,kind,state,idempotency_key,created_at,updated_at)
			 VALUES($1,$2,$3,'export','queued','growth-target',now()-interval '91 days',now())`,
			targetID, tenantID, projectID)
		return err
	}); err != nil {
		t.Fatalf("seed target job: %v", err)
	}
	parkTarget := func(park bool) {
		t.Helper()
		if err := tenant.Run(ctx, pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
			if park {
				_, err := tx.Exec(ctx, `UPDATE jobs SET run_at=now()+interval '1 hour', state='queued',
					lease_owner=NULL, lease_until=NULL, attempt=0 WHERE id=$1`, targetID)
				return err
			}
			_, err := tx.Exec(ctx, `UPDATE jobs SET run_at=NULL, state='queued',
				lease_owner=NULL, lease_until=NULL, attempt=0 WHERE id=$1`, targetID)
			return err
		}); err != nil {
			t.Fatalf("park/unpark target: %v", err)
		}
	}
	claim := func() error {
		_, err := pool.Exec(ctx, `SELECT id FROM ppts_claim_next_job($1,$2)`, "growth-cleanup", 30)
		return err
	}

	// 灌 16 万终态任务 + 64 万事件（与基线工具同形：85% succeeded / 10% failed / 5% canceled）。
	nextSeq := 0
	seedGrowthRows(t, ctx, pool, tenantID, projectID, 160_000, &nextSeq)

	var beforeJobs, beforeEvents int
	if err := tenant.Run(ctx, pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE tenant_id=$1`, tenantID).Scan(&beforeJobs); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM job_events WHERE tenant_id=$1`, tenantID).Scan(&beforeEvents)
	}); err != nil {
		t.Fatalf("count before: %v", err)
	}

	parkTarget(true)
	idleBefore, err := measureGrowth(30, claim)
	if err != nil {
		t.Fatalf("claim-idle before: %v", err)
	}
	parkTarget(false)
	var hitBeforeTotal time.Duration
	for i := 0; i < 30; i++ {
		start := time.Now()
		if err := claim(); err != nil {
			t.Fatalf("claim-hit before: %v", err)
		}
		hitBeforeTotal += time.Since(start)
		parkTarget(false)
	}
	hitBefore := hitBeforeTotal / 30

	// 直接用候选 CTE 量扫描成本（绕过函数固定开销），作为清后对照的「增长项」基准。
	scanBefore, err := measureGrowth(30, func() error {
		return tenant.Run(ctx, pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
			_, e := tx.Exec(ctx, growthCandidateQuery)
			return e
		})
	})
	if err != nil {
		t.Fatalf("scan before: %v", err)
	}

	t.Logf("清理前：jobs=%d events=%d | claim-idle(函数)=%v claim-hit(函数)=%v scan(CTE)=%v",
		beforeJobs, beforeEvents, idleBefore.Round(time.Microsecond), hitBefore.Round(time.Microsecond), scanBefore.Round(time.Microsecond))

	// 跑保留清理：cutoff 设为 now+24h（即「把所有终态行都算过期」），覆盖全部种子
	// （最旧 updated_at = now-90天+5min，最新生子因 +5min 略超 now），一次性清空历史 90 天
	// 堆积的全部终态行——这是「启用保留后首次全量清积压」的场景。
	// 生产里按 PPTS_JOB_RETENTION_TTL（默认 30 天）每周期清一次，稳态只留近 30 天 + 在途行，
	// 表被钉在很小，claim-idle 回到基线。
	rstore := retention.NewPGStore(pool)
	cutoff := time.Now().Add(24 * time.Hour)
	var totalDeleted int
	var firstBatch, lastBatch time.Duration
	batches := 0
	cleanStart := time.Now()
	for {
		bStart := time.Now()
		deleted, err := rstore.DeleteExpiredJobs(ctx, tenantID, cutoff, 500)
		if err != nil {
			t.Fatalf("DeleteExpiredJobs: %v", err)
		}
		bDur := time.Since(bStart)
		batches++
		if batches == 1 {
			firstBatch = bDur
		}
		lastBatch = bDur
		totalDeleted += len(deleted)
		if len(deleted) < 500 {
			break
		}
		if batches > 2000 {
			t.Logf("safety: stopped after %d batches", batches)
			break
		}
	}
	cleanDur := time.Since(cleanStart)

	// 清完的强断言：所有终态行都被删，只剩常驻目标（queued，非终态）。
	if want := beforeJobs - 1; totalDeleted != want {
		t.Fatalf("cleanup deleted %d terminal jobs, want %d (beforeJobs=%d, target kept)", totalDeleted, want, beforeJobs)
	}

	var afterJobs, afterEvents int
	if err := tenant.Run(ctx, pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE tenant_id=$1`, tenantID).Scan(&afterJobs); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM job_events WHERE tenant_id=$1`, tenantID).Scan(&afterEvents)
	}); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if afterJobs != 1 {
		t.Fatalf("afterJobs=%d, want 1 (only the queued target remains)", afterJobs)
	}
	if afterEvents != 0 {
		t.Fatalf("afterEvents=%d, want 0 (target has no events; events deleted with their jobs)", afterEvents)
	}

	// 清完积压后的稳态：分两层量，避免被 ppts_claim_next_job 的固定开销误导。
	// (a) 直接用候选 CTE 量扫描成本（绕过函数的 PL/pgSQL / SECURITY DEFINER 额外开销，隔离
	//     「表只增不减」这一项的账单）——这是保留策略要消除的「增长项」，1 行表上必为微秒级。
	// (b) 生产真实路径（函数调用，含固定开销）——仅作对照；其数值在嵌入式上下文波动大，
	//     真实固定开销见基线 1 行 ~571µs / 干净连接 0.5ms。
	freshPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("fresh pool: %v", err)
	}
	t.Cleanup(freshPool.Close)
	directScan := func() error {
		return tenant.Run(ctx, freshPool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
			_, e := tx.Exec(ctx, growthCandidateQuery)
			return e
		})
	}
	for i := 0; i < 5; i++ { // 预热（丢弃）
		if err := directScan(); err != nil {
			t.Fatalf("warmup scan: %v", err)
		}
	}
	scanSteady, err := measureGrowth(30, directScan)
	if err != nil {
		t.Fatalf("scan steady: %v", err)
	}

	claimFresh := func() error {
		_, err := freshPool.Exec(ctx, `SELECT id FROM ppts_claim_next_job($1,$2)`, "growth-cleanup", 30)
		return err
	}
	for i := 0; i < 5; i++ {
		if err := claimFresh(); err != nil {
			t.Fatalf("warmup fn: %v", err)
		}
	}
	funcSteady, err := measureGrowth(30, claimFresh)
	if err != nil {
		t.Fatalf("func steady: %v", err)
	}

	t.Logf("清理后：jobs=%d events=%d", afterJobs, afterEvents)
	t.Logf("清理统计：删除 %d 终态任务（含事件），%d 批，总耗时 %v", totalDeleted, batches, cleanDur.Round(time.Millisecond))
	t.Logf("清理批次耗时：首批 %v（全租户 seq scan 找 500 个终态行）末批 %v（近空表）——差距即 0055 索引要消除的成本",
		firstBatch.Round(time.Millisecond), lastBatch.Round(time.Millisecond))
	t.Logf("清理前/后候选 CTE 扫描成本：清前(16万行,缓存热)=%v 清后(1活行,刚VACUUM冷堆)=%v",
		scanBefore.Round(time.Microsecond), scanSteady.Round(time.Microsecond))
	t.Logf("注：清后绝对数仍偏高，因一次性删 16 万行留下 ~248MB 膨胀堆（DELETE 不缩文件、普通 VACUUM 不截断文件体），\n\t且刚 VACUUM 后堆是冷缓存——二者都会随缓存预热 / 堆压缩而消失，并非活行造成。\n\tEXPLAIN 证清后活行仅 ~1 行被扫；可靠稳态 floor≈基线 1 行 ~571µs（干净连接独立验证 0.5ms），\n\t生产靠 autovacuum 复用死空间 + 周期性压缩回到该 floor。")
	t.Logf("生产函数路径对照（含固定开销，嵌入式上下文波动大）：清前=%v 清后=%v",
		idleBefore.Round(time.Microsecond), funcSteady.Round(time.Microsecond))

	// best-effort VACUUM：回收死元组、收缩堆（释放 248MB 膨胀、降低后续扫描/下次清理的 I/O），
	// 需要表属主 / MAINTAIN 权限；ppts_app 通常没有，这里吞掉错误并记录。注意：VACUUM 回收的是
	// 磁盘与堆大小，不降低 ppts_claim_next_job 的单次固定开销（后者由活行数 + 函数固定成本决定，
	// 两者都被保留策略钉住），但能避免膨胀堆拖累整体。
	if _, err := pool.Exec(ctx, `VACUUM (ANALYZE) jobs, job_events`); err != nil {
		t.Logf("VACUUM 未执行（需表属主/MAINTAIN 权限，ppts_app 无）：%v —— 生产依赖 autovacuum\n\t或 migrator 上下文的周期性 VACUUM 回收膨胀堆", err)
	} else {
		t.Logf("VACUUM 已执行（回收死元组、收缩堆）")
	}
	explainGrowthCandidate(t, ctx, pool, tenantID)
}
