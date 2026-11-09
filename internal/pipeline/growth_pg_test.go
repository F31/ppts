//go:build pg

package pipeline

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/tenant"
)

// TestPGJobTableGrowthBaseline 是 jobs/job_events 保留策略的**增长基线工具**：
// 按 0 / 1 万 / 5 万 / 10 万累计终态任务逐级灌数据，在每个规模量四条路径的平均延迟——
//
//	claim-idle ：调度器空转轮询（没有可领取任务时 ppts_claim_next_job 的成本；
//	             这是频率最高的一条：调度器无论有没有活都在周期性跑）
//	claim-hit  ：真实领取一次（领取后立刻复位，测的是完整扫描 + 排序 + 行锁）
//	list       ：任务中心列表（List，keyset 分页取 20 条）
//	events     ：WatchEvents 断点续传（EventsSince 取最近 200 条）
//	insert     ：写一条 job_events（含 RLS 事务开销，与 recordJobEvent 同形）
//
// 灌入的行贴近真实形态：85% succeeded / 10% failed / 5% canceled（终态，永不再被调度），
// created_at 铺满过去 90 天，每任务 4 条事件快照（每条 ~700B）。
//
// 默认跳过：必须同时设 PPTS_GROWTH_BASELINE=1 与 PPTS_TEST_DATABASE 才会执行
// （一次全量要灌 16 万任务 + 64 万事件、约 1GB，不适合进常规测试轮）。
// 跑法：PPTS_GROWTH_BASELINE=1 PPTS_TEST_DATABASE=... go test -tags=pg ./internal/pipeline/ -run TestPGJobTableGrowthBaseline -v
func TestPGJobTableGrowthBaseline(t *testing.T) {
	if os.Getenv("PPTS_GROWTH_BASELINE") == "" {
		t.Skip("set PPTS_GROWTH_BASELINE=1 to run the growth baseline (seeds 160k jobs / 640k events, ~1GB)")
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
	store, err := NewPGStore(ctx, dsn)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { store.pool.Close() })

	tenantID := uuid.NewString()
	projectID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES($1,'growth-baseline')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if err := tenant.Run(ctx, pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO projects(id,tenant_id,owner_user,title) VALUES($1,$2,'tester','growth-baseline')`,
			projectID, tenantID)
		return err
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	t.Cleanup(func() {
		// job_events 对 tenants 有外键且不级联，先删；jobs 级联 job_steps。
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

	// 领取目标：全场最老的 eligible 任务（91 天前入队），保证 claim-hit 每次都领到它。
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
	// claim 后复位（把目标放回队列），复位本身不计时。
	resetTarget := func() {
		t.Helper()
		parkTarget(false)
	}

	claim := func() error {
		_, err := pool.Exec(ctx, `SELECT id FROM ppts_claim_next_job($1,$2)`, "growth-baseline", 30)
		return err
	}

	nextSeq := 0
	t.Logf("| 累计任务 | 累计事件 | claim-idle | claim-hit | list(20) | events(200) | insert |")
	t.Logf("|---|---|---|---|---|---|---|")
	for _, n := range []int{0, 10_000, 50_000, 100_000} {
		seedGrowthRows(t, ctx, pool, tenantID, projectID, n, &nextSeq)

		var jobCount, eventCount int
		if err := tenant.Run(ctx, pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE tenant_id=$1`, tenantID).Scan(&jobCount); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `SELECT count(*) FROM job_events WHERE tenant_id=$1`, tenantID).Scan(&eventCount)
		}); err != nil {
			t.Fatalf("count: %v", err)
		}

		// 空转轮询：目标不可领取，claim 返回空——这是调度器的心跳成本。
		parkTarget(true)
		idle, err := measureGrowth(30, claim)
		if err != nil {
			t.Fatalf("claim-idle: %v", err)
		}

		// 真实领取（只计时 claim；领取后的事务性复位不计时，但每轮必须做，
		// 否则第二轮起目标已带租约、claim 会返回空，量到的就不是领取成本）。
		parkTarget(false)
		var hitTotal time.Duration
		for i := 0; i < 30; i++ {
			start := time.Now()
			if err := claim(); err != nil {
				t.Fatalf("claim-hit: %v", err)
			}
			hitTotal += time.Since(start)
			resetTarget()
		}
		hit := hitTotal / 30

		list, err := measureGrowth(30, func() error {
			_, _, err := store.List(ctx, tenantID, projectID, "", "", 20)
			return err
		})
		if err != nil {
			t.Fatalf("list: %v", err)
		}

		var lastSeq int64
		if err := tenant.Run(ctx, pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx,
				`SELECT coalesce(max(seq),0) FROM job_events WHERE tenant_id=$1 AND project_id=$2`,
				tenantID, projectID).Scan(&lastSeq)
		}); err != nil {
			t.Fatalf("max seq: %v", err)
		}
		events, err := measureGrowth(30, func() error {
			_, err := store.EventsSince(ctx, tenantID, projectID, lastSeq-200, 200)
			return err
		})
		if err != nil {
			t.Fatalf("events: %v", err)
		}

		insert, err := measureGrowth(30, func() error {
			return tenant.Run(ctx, pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(ctx,
					`INSERT INTO job_events (tenant_id, project_id, job_id, state, progress, snapshot)
					 VALUES ($1,$2,$3,'succeeded',100,$4)`,
					tenantID, projectID, targetID, growthEventSnapshot(targetID, 100))
				return err
			})
		})
		if err != nil {
			t.Fatalf("insert: %v", err)
		}

		t.Logf("| %d | %d | %v | %v | %v | %v | %v |",
			jobCount, eventCount, idle.Round(time.Microsecond), hit.Round(time.Microsecond),
			list.Round(time.Microsecond), events.Round(time.Microsecond), insert.Round(time.Microsecond))
	}

	var jobsSize, eventsSize string
	if err := pool.QueryRow(ctx, `SELECT pg_size_pretty(pg_total_relation_size('jobs'))`).Scan(&jobsSize); err == nil {
		if err := pool.QueryRow(ctx, `SELECT pg_size_pretty(pg_total_relation_size('job_events'))`).Scan(&eventsSize); err == nil {
			t.Logf("表体积（含索引）：jobs %s，job_events %s", jobsSize, eventsSize)
		}
	}
	explainGrowthCandidate(t, ctx, pool, tenantID)
}

// seedGrowthRows 灌入 n 条终态任务（created_at 铺满过去 90 天）与每任务 4 条事件快照。
// nextSeq 是跨规模递增的幂等键序号，保证 UNIQUE(tenant_id, idempotency_key, kind) 不冲突。
func seedGrowthRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenantID, projectID string, n int, nextSeq *int) {
	t.Helper()
	if n == 0 {
		return
	}
	start := time.Now()
	err := tenant.Run(ctx, pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		jobCols := []string{"id", "tenant_id", "project_id", "kind", "state",
			"input_snapshot", "idempotency_key", "attempt", "fencing_token", "progress", "created_at", "updated_at"}
		eventCols := []string{"tenant_id", "project_id", "job_id", "state", "progress", "snapshot", "created_at"}
		const batch = 1000
		now := time.Now()
		ids := make([]string, 0, n)
		created := make([]time.Time, 0, n)
		var rows [][]any
		flushJobs := func() error {
			if len(rows) == 0 {
				return nil
			}
			// COPY FROM 被 FORCE RLS 拒绝（SQLSTATE 0A000），用多值 INSERT 批量灌。
			if err := insertRows(ctx, tx, "jobs", jobCols, rows, batch); err != nil {
				return fmt.Errorf("seed jobs: %w", err)
			}
			rows = rows[:0]
			return nil
		}
		for i := 0; i < n; i++ {
			id := uuid.NewString()
			ids = append(ids, id)
			// 90 天跨度按 i 线性分布。必须用 float64 做中间乘法：90 天的纳秒值（~7.8e15）
			// 乘以 16 万行会远超 int64 上限（9.2e18）而回绕成负数/垃圾，使 created_at/
			// updated_at 失真（部分落到未来，又让「按 updated_at 清理」漏删）。
			at := now.Add(-time.Duration(float64(90*24*time.Hour) * float64(i) / float64(n)))
			created = append(created, at)
			state, kind := "succeeded", "narration"
			switch i % 10 {
			case 7:
				state = "canceled"
			case 8, 9:
				state = "failed"
			}
			switch i % 5 {
			case 0:
				kind = "export"
			case 1:
				kind = "script_draft"
			case 2:
				kind = "render"
			case 3:
				kind = "parse"
			}
			rows = append(rows, []any{id, tenantID, projectID, kind, state,
				growthInputSnapshot(), fmt.Sprintf("growth-%08d", *nextSeq+i), 1, 1, 100, at, at.Add(5 * time.Minute)})
			if len(rows) >= batch {
				if err := flushJobs(); err != nil {
					return err
				}
			}
		}
		if err := flushJobs(); err != nil {
			return err
		}
		*nextSeq += n

		eventStates := []string{"queued", "running", "running", "succeeded"}
		eventProgress := []int{0, 30, 80, 100}
		var erows [][]any
		flushEvents := func() error {
			if len(erows) == 0 {
				return nil
			}
			if err := insertRows(ctx, tx, "job_events", eventCols, erows, batch); err != nil {
				return fmt.Errorf("seed job_events: %w", err)
			}
			erows = erows[:0]
			return nil
		}
		for i, id := range ids {
			for k := 0; k < 4; k++ {
				erows = append(erows, []any{tenantID, projectID, id, eventStates[k], eventProgress[k],
					growthEventSnapshot(id, eventProgress[k]), created[i].Add(time.Duration(k) * time.Minute)})
			}
			if len(erows) >= batch {
				if err := flushEvents(); err != nil {
					return err
				}
			}
		}
		return flushEvents()
	})
	if err != nil {
		t.Fatalf("seed %d rows: %v", n, err)
	}
	t.Logf("灌入 %d 任务 + %d 事件，用时 %v", n, n*4, time.Since(start).Round(time.Millisecond))
}

// insertRows 用多值 INSERT 批量灌行（COPY FROM 被 FORCE RLS 拒绝，SQLSTATE 0A000）。
// batchSize 上限受占位符数（65535）约束：1000 行 × 12 列 = 12000，安全。
func insertRows(ctx context.Context, tx pgx.Tx, table string, cols []string, rows [][]any, batchSize int) error {
	n := len(cols)
	for start := 0; start < len(rows); start += batchSize {
		end := start + batchSize
		if end > len(rows) {
			end = len(rows)
		}
		var sb strings.Builder
		sb.WriteString("INSERT INTO ")
		sb.WriteString(table)
		sb.WriteByte('(')
		sb.WriteString(strings.Join(cols, ","))
		sb.WriteString(") VALUES ")
		args := make([]any, 0, (end-start)*n)
		for i := start; i < end; i++ {
			if i > start {
				sb.WriteByte(',')
			}
			sb.WriteByte('(')
			for j := 0; j < n; j++ {
				if j > 0 {
					sb.WriteByte(',')
				}
				sb.WriteByte('$')
				sb.WriteString(strconv.Itoa(len(args) + 1))
				args = append(args, rows[i][j])
			}
			sb.WriteByte(')')
		}
		if _, err := tx.Exec(ctx, sb.String(), args...); err != nil {
			return err
		}
	}
	return nil
}

// growthInputSnapshot 造一份 ~800B 的任务输入快照（30 页导出的典型量级）。
func growthInputSnapshot() string {
	return fmt.Sprintf(`{"format":"mp4","revision":"rev-20261108","slides":["%s"],"options":{"fps":30,"width":1920,"height":1080,"bitrateKbps":4000}}`,
		strings.Repeat("slide-id-", 70))
}

// growthEventSnapshot 造一份 ~700B 的事件快照（Job 序列化后的典型量级）。
func growthEventSnapshot(jobID string, progress int) string {
	return fmt.Sprintf(`{"id":%q,"kind":"export","state":"succeeded","progress":%d,"attempt":1,"idempotencyKey":"","inputSnapshot":"%s","lastError":null,"traceparent":"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}`,
		jobID, progress, strings.Repeat("s", 480))
}

// measureGrowth 跑 iters 次取平均单次耗时（第一轮的结果也计入：真实路径没有预热豁免）。
func measureGrowth(iters int, fn func() error) (time.Duration, error) {
	var total time.Duration
	for i := 0; i < iters; i++ {
		start := time.Now()
		if err := fn(); err != nil {
			return 0, err
		}
		total += time.Since(start)
	}
	return total / time.Duration(iters), nil
}

// explainGrowthCandidate 打印 ppts_claim_next_job 候选 CTE 的真实执行计划。
// 函数本身是 SECURITY DEFINER，EXPLAIN 只能看到 Function Scan；且 0013 给 ppts_migrator
// 配了 USING(true) 策略——函数内 RLS 实际被放开，扫描的是**全部租户**的行。这里在租户
// 上下文里把函数体里的候选查询原样抽出来解释（基准数据全在该租户下，行数等价于全局），
// 排序节点与扫描行数才是「表只增不减」的账单。
func explainGrowthCandidate(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenantID string) {
	t.Helper()
	err := tenant.Run(ctx, pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF)
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
SELECT * FROM candidate`)
		if err != nil {
			return err
		}
		defer rows.Close()
		t.Logf("---- ppts_claim_next_job 候选 CTE 的执行计划（租户上下文） ----")
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			t.Logf("    %s", line)
		}
		return rows.Err()
	})
	if err != nil {
		t.Logf("explain: %v", err)
	}
}
