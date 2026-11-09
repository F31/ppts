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

// TestPGJobTableGrowthCleanup 验证「首次把历史积压一次清光」的正确性（P3-6）。
// 把一个租户灌到 16 万终态任务（与基线工具终态分布一致），cutoff 取 now+24h，
// 断言终态行和事件被删光、常驻的 queued 目标还在。
//
// 批次耗时只作日志。首批慢、末批快混有页缓存变热：job_events 的堆在 DELETE 之后
// 不会变小，末批仍然可能在扫同一份已经进缓存的堆。这不是迁移 0055 的实测。
// 索引是否被用、30 天窗口留下多少行，见 TestPGJobTableGrowthRetentionWindow。
// 清完之后的墙钟也不回到 1 行基线的 571µs：一次性删 16 万行留下的膨胀文件还在。
//
// 默认跳过：设 PPTS_GROWTH_CLEANUP=1 与 PPTS_TEST_DATABASE 才执行（一次灌 16 万任务 +
// 64 万事件、约 1GB）。与基线工具、30 天窗口 demo 用不同的门控，避免叠加成本。
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

	// 跑保留清理：cutoff 设为 now+24h（即「把所有终态行都算过期」），覆盖全部种子。
	// 这是「启用保留后首次全量清积压」。生产的 30 天窗口留下大约三分之一，
	// 行数和执行计划在 TestPGJobTableGrowthRetentionWindow 里断言。
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
	t.Logf("清理批次耗时：首批 %v 末批 %v（墙钟，含页缓存变热；不是 0055 的对照）",
		firstBatch.Round(time.Millisecond), lastBatch.Round(time.Millisecond))
	t.Logf("清理前/后候选 CTE 扫描成本：清前(16万行)=%v 清后(1活行,尚未 VACUUM)=%v",
		scanBefore.Round(time.Microsecond), scanSteady.Round(time.Microsecond))
	t.Logf("注：下面的清后读数取自 VACUUM 之前。一次性删 16 万行会留下膨胀文件（DELETE 不缩文件、普通 VACUUM 也不把文件截断还给操作系统），\n\t清后墙钟因此回不到 1 行基线的 571µs。这个数不是活行的账单，也不拿来证明保留策略。\n\t稳态靠 autovacuum 复用死空间；文件要缩回去是维护窗口里的一次性 VACUUM FULL / pg_repack，不放进 worker。")
	t.Logf("生产函数路径对照（含固定开销，嵌入式上下文波动大）：清前=%v 清后=%v",
		idleBefore.Round(time.Microsecond), funcSteady.Round(time.Microsecond))

	// 测量已经结束。这里的 VACUUM 只尝试回收死元组，不重新计时：普通 VACUUM 不把文件
	// 截断还给操作系统，也不能拿来解释上面的清后读数。ppts_app 通常不是表属主，失败就记下。
	// 不在 worker 里周期执行 VACUUM。
	if _, err := pool.Exec(ctx, `VACUUM (ANALYZE) jobs, job_events`); err != nil {
		t.Logf("VACUUM 未执行（ppts_app 通常不是表属主）：%v", err)
	} else {
		t.Logf("VACUUM 已执行。死元组可被复用；文件大小不变，所以上面的清后读数仍然有效")
	}
	explainGrowthCandidate(t, ctx, pool, tenantID)
}
