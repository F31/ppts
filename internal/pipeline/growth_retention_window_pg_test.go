//go:build pg

package pipeline

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/retention"
	"github.com/F31/ppts/internal/tenant"
)

// TestPGJobTableGrowthRetentionWindow 按生产的 30 天 TTL 清一轮 16 万终态任务。
//
// 种子的 updated_at 铺满约 90 天，所以大约三分之一留在窗口里（约 5.3 万行），
// 不是清成 1 行。验收是三件事：留下的行数、删除语句的执行计划用了迁移 0055
// 的两份索引、热缓存下的 buffer 读数。claim-idle 的墙钟只写日志——6 万行的基线
// 大约 9ms，膨胀文件还会把它抬高，不拿 571µs 当通过条件。
//
// 计划依赖 ANALYZE 之后的统计。ppts_app 通常不能 ANALYZE，设
// PPTS_TEST_ADMIN_DATABASE 为表属主或超级用户。索引不在时直接失败，不灌 16 万行。
//
// 默认跳过。跑法：
//
//	PPTS_GROWTH_RETENTION_WINDOW=1 PPTS_TEST_DATABASE=... PPTS_TEST_ADMIN_DATABASE=... \
//	  go test -tags=pg ./internal/pipeline/ -run TestPGJobTableGrowthRetentionWindow -v -timeout 45m
func TestPGJobTableGrowthRetentionWindow(t *testing.T) {
	if os.Getenv("PPTS_GROWTH_RETENTION_WINDOW") == "" {
		t.Skip("set PPTS_GROWTH_RETENTION_WINDOW=1 to run the 30-day retention window demo")
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

	var indexes int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_indexes
		 WHERE indexname IN ('idx_jobs_retention', 'idx_job_events_job')`).Scan(&indexes); err != nil {
		t.Fatalf("list retention indexes: %v", err)
	}
	if indexes != 2 {
		t.Fatalf("migration 0055 is not applied (found %d of idx_jobs_retention, idx_job_events_job); apply migrations/0055_jobs_retention_indexes.sql as the table owner first", indexes)
	}
	// 先确认 ANALYZE 跑得通，再灌 16 万行。统计要在灌完之后再刷一次。
	if err := analyzeGrowthTables(t, ctx, pool); err != nil {
		t.Fatalf("ANALYZE before seed: %v（ppts_app 通常不能 ANALYZE，设 PPTS_TEST_ADMIN_DATABASE 为表属主或超级用户）", err)
	}

	tenantID := uuid.NewString()
	projectID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES($1,'growth-window')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if err := tenant.Run(ctx, pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO projects(id,tenant_id,owner_user,title) VALUES($1,$2,'tester','growth-window')`,
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

	// 常驻 queued：91 天前创建，但不是终态，30 天清理不会碰它。
	targetID := uuid.NewString()
	if err := tenant.Run(ctx, pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO jobs(id,tenant_id,project_id,kind,state,idempotency_key,created_at,updated_at)
			 VALUES($1,$2,$3,'export','queued','growth-window-target',now()-interval '91 days',now())`,
			targetID, tenantID, projectID)
		return err
	}); err != nil {
		t.Fatalf("seed target job: %v", err)
	}

	nextSeq := 0
	seedGrowthRows(t, ctx, pool, tenantID, projectID, 160_000, &nextSeq)
	if err := analyzeGrowthTables(t, ctx, pool); err != nil {
		t.Fatalf("ANALYZE: %v", err)
	}

	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	var doomed, kept, live int
	if err := tenant.Run(ctx, pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT
				count(*) FILTER (WHERE state IN ('succeeded','failed','canceled') AND updated_at < $2),
				count(*) FILTER (WHERE state IN ('succeeded','failed','canceled') AND updated_at >= $2),
				count(*) FILTER (WHERE state NOT IN ('succeeded','failed','canceled'))
			FROM jobs WHERE tenant_id=$1`, tenantID, cutoff).Scan(&doomed, &kept, &live)
	}); err != nil {
		t.Fatalf("count window: %v", err)
	}
	if live != 1 {
		t.Fatalf("non-terminal jobs = %d, want 1 (the queued target)", live)
	}
	if doomed+kept != 160_000 {
		t.Fatalf("terminal jobs = %d, want 160000 (doomed=%d kept=%d)", doomed+kept, doomed, kept)
	}
	// 90 天均匀铺开、留 30 天：大约三分之一。带宽盖住种子耗时带来的截止时间位移，
	// 拦得住「全删」和「一行不删」。
	if kept < 40_000 || kept > 70_000 {
		t.Fatalf("kept terminal jobs = %d, want about 1/3 of 160000", kept)
	}
	t.Logf("窗口预告：cutoff=%s doomed=%d kept=%d live=%d", cutoff.Format(time.RFC3339), doomed, kept, live)

	for round := 1; round <= 2; round++ {
		start := time.Now()
		plan := explainDeleteExpiredJobs(t, ctx, pool, tenantID, cutoff)
		t.Logf("---- EXPLAIN ANALYZE 第 %d 轮（%v，事务已回滚） ----\n%s", round, time.Since(start).Round(time.Millisecond), plan)
		if strings.Contains(plan, "Seq Scan on jobs") || strings.Contains(plan, "Seq Scan on job_events") {
			t.Fatalf("round %d still seq-scans jobs or job_events:\n%s", round, plan)
		}
		if !strings.Contains(plan, "idx_jobs_retention") || !strings.Contains(plan, "idx_job_events_job") {
			t.Fatalf("round %d plan does not use both retention indexes:\n%s", round, plan)
		}
	}

	rstore := retention.NewPGStore(pool)
	var totalDeleted int
	var firstBatch, secondBatch time.Duration
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
		switch batches {
		case 1:
			firstBatch = bDur
		case 2:
			secondBatch = bDur
		}
		totalDeleted += len(deleted)
		if len(deleted) < 500 {
			break
		}
		if batches > 400 {
			t.Fatalf("safety: still full batches after %d (deleted %d)", batches, totalDeleted)
		}
	}
	t.Logf("30 天清理：删除 %d，%d 批，总耗时 %v，首批 %v，第二批 %v",
		totalDeleted, batches, time.Since(cleanStart).Round(time.Millisecond),
		firstBatch.Round(time.Millisecond), secondBatch.Round(time.Millisecond))
	if totalDeleted != doomed {
		t.Fatalf("deleted %d, want %d terminal jobs older than the cutoff", totalDeleted, doomed)
	}

	var afterJobs, afterEvents, stillOld int
	if err := tenant.Run(ctx, pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE tenant_id=$1`, tenantID).Scan(&afterJobs); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM job_events WHERE tenant_id=$1`, tenantID).Scan(&afterEvents); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM jobs
			 WHERE tenant_id=$1 AND state IN ('succeeded','failed','canceled') AND updated_at < $2`,
			tenantID, cutoff).Scan(&stillOld)
	}); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if stillOld != 0 {
		t.Fatalf("terminal jobs still older than cutoff: %d", stillOld)
	}
	if afterJobs != kept+live {
		t.Fatalf("jobs after = %d, want %d kept terminal + %d live", afterJobs, kept, live)
	}
	if afterEvents != kept*4 {
		t.Fatalf("events after = %d, want %d (4 per kept terminal job; the queued target has none)", afterEvents, kept*4)
	}
	var targetState string
	if err := tenant.Run(ctx, pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT state FROM jobs WHERE id=$1`, targetID).Scan(&targetState)
	}); err != nil {
		t.Fatalf("queued target: %v", err)
	}
	if targetState != "queued" {
		t.Fatalf("queued target state = %q", targetState)
	}
	t.Logf("窗口结果：jobs=%d events=%d（约 5.3 万终态 + 1 条 queued）。claim-idle 预期靠近 6 万行基线的 ~9ms，不是 571µs", afterJobs, afterEvents)

	park := func() error {
		return tenant.Run(ctx, pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE jobs SET run_at=now()+interval '1 hour', state='queued',
				lease_owner=NULL, lease_until=NULL, attempt=0 WHERE id=$1`, targetID)
			return err
		})
	}
	if err := park(); err != nil {
		t.Fatalf("park target: %v", err)
	}
	idle, err := measureGrowth(30, func() error {
		_, err := pool.Exec(ctx, `SELECT id FROM ppts_claim_next_job($1,$2)`, "growth-window", 30)
		return err
	})
	if err != nil {
		t.Fatalf("claim-idle: %v", err)
	}
	t.Logf("清后 claim-idle（函数，只记录不设阈值）=%v", idle.Round(time.Microsecond))
}

// analyzeGrowthTables 在看计划之前刷新 jobs / job_events 的统计。
// 刚灌进 16 万行时，规划器若还拿着空表统计，会继续选顺序扫描。
func analyzeGrowthTables(t *testing.T, ctx context.Context, app *pgxpool.Pool) error {
	t.Helper()
	run := func(p *pgxpool.Pool) error {
		_, err := p.Exec(ctx, `ANALYZE jobs, job_events`)
		return err
	}
	if adminDSN := os.Getenv("PPTS_TEST_ADMIN_DATABASE"); adminDSN != "" {
		admin, err := pgxpool.New(ctx, adminDSN)
		if err != nil {
			return err
		}
		defer admin.Close()
		err = run(admin)
		if err == nil {
			t.Logf("ANALYZE via PPTS_TEST_ADMIN_DATABASE")
		}
		return err
	}
	err := run(app)
	if err == nil {
		t.Logf("ANALYZE via PPTS_TEST_DATABASE")
	}
	return err
}

// explainDeleteExpiredJobs 在一个会回滚的事务里跑 EXPLAIN ANALYZE，不留下删除。
func explainDeleteExpiredJobs(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenantID string, cutoff time.Time) string {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin explain: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
		t.Fatalf("set tenant for explain: %v", err)
	}
	rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+retention.DeleteExpiredJobsSQL, tenantID, cutoff, 500)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	var b strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("explain scan: %v", err)
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("explain rows: %v", err)
	}
	rows.Close()
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback explain: %v", err)
	}
	return b.String()
}
