package pipeline

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/tenant"
	"github.com/F31/ppts/internal/traceprop"
)

// PGStore 以 PostgreSQL 实现 Store（V4.0 §10.2）：
// SKIP LOCKED 领取 + 原子租约/fencing + 事务外执行 + fencing 条件提交。
// 所有数据访问在租户上下文（app.tenant_id，事务局部）内执行，受 RLS 约束。
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore 创建连接池并 ping。
func NewPGStore(ctx context.Context, dsn string) (*PGStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &PGStore{pool: pool}, nil
}

// Close 释放连接池。
func (s *PGStore) Close() { s.pool.Close() }

// （列清单 jobSelectColumns 由 columns.go 的 jobColumns 表生成；PG 侧不含自己的副本。）

// jobScanBuf 承载 jobSelectColumns 的扫描目标。
// 抽成结构体是为了让「列清单 + 追加列」（如列表页需要 phase 与受影响页数）复用同一份扫描逻辑，
// 避免并行维护两份 17 列顺序——列顺序一旦错位，错误会以「类型不匹配」的面目出现在很远的地方。
type jobScanBuf struct {
	j             Job
	createdAt     time.Time
	updatedAt     time.Time
	leaseOwner    *string
	runAtPtr      *time.Time
	leaseUntilPtr *time.Time
	lastErr       []byte
}

// dest 返回与 jobSelectColumns 严格同序的扫描目标；调用方可直接在其后追加额外列的指针。
// 顺序来自 columns.go 的 jobColumns 表（与 SELECT 文本同源），此处不再手写第二份。
func (b *jobScanBuf) dest() []any {
	return pgJobDest(jobColumns, b)
}

// job 把扫描到的原始值整理成 *Job（可空列合并、last_error 反序列化）。
func (b *jobScanBuf) job() *Job {
	if b.leaseOwner != nil {
		b.j.LeaseOwner = *b.leaseOwner
	}
	if b.leaseUntilPtr != nil {
		b.j.LeaseUntil = *b.leaseUntilPtr
	}
	if b.runAtPtr != nil {
		b.j.RunAt = *b.runAtPtr
	}
	b.j.CreatedAt, b.j.UpdatedAt = b.createdAt, b.updatedAt
	if len(b.lastErr) > 0 {
		b.j.LastError = &JobError{}
		_ = json.Unmarshal(b.lastErr, b.j.LastError)
	}
	return &b.j
}

func scanJob(row pgx.Row) (*Job, error) {
	b := &jobScanBuf{}
	if err := row.Scan(b.dest()...); err != nil {
		return nil, err
	}
	return b.job(), nil
}

// scanJobPageRow 扫描「核心投影 + phase + 受影响页数」（B4-M6b 列表页专用列）。
func scanJobPageRow(row pgx.Row) (JobPageRow, error) {
	b := &jobScanBuf{}
	var phase string
	var pageCount int
	if err := row.Scan(append(b.dest(), &phase, &pageCount)...); err != nil {
		return JobPageRow{}, err
	}
	return JobPageRow{Job: b.job(), Phase: phase, PageCount: pageCount}, nil
}

// Create idempotent：同 (tenant_id, idempotency_key, kind) 命中唯一约束时返回既有行。
//
// affected_pages（B4-M6b）在入队时由 input_snapshot 提取落库：快照一旦创建即不再变化，
// 因此该列与之后由快照推导的范围**必然一致**；它的价值是让「按受影响页数排序/计数」可在 SQL 内完成
// （input_snapshot 是 text 且字段名随 kind 变化，无法在 SQL 内解析）。
func (s *PGStore) Create(ctx context.Context, tenantID, projectID, kind, idemKey, snapshot string, runAt time.Time) (*Job, error) {
	var j *Job
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `INSERT INTO jobs
			(id, tenant_id, project_id, kind, state, idempotency_key, input_snapshot, run_at, traceparent, affected_pages)
			VALUES (gen_random_uuid(),$1,$2,$3,'queued',$4,$5,$6,$7,$8::jsonb)
			ON CONFLICT (tenant_id, idempotency_key, kind) DO NOTHING
			RETURNING `+jobSelectColumns,
			tenantID, projectID, kind, idemKey, snapshot, nullableTime(&runAt), traceprop.FromContext(ctx),
			mustJSONArray(AffectedPagesOf(JobKind(kind), snapshot)))
		got, err := scanJob(row)
		if err == nil {
			j = got
			return recordJobEvent(ctx, tx, j)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// 命中幂等约束：返回既有行，不重复写事件。
		got, err = scanJob(tx.QueryRow(ctx,
			"SELECT "+jobSelectColumns+" FROM jobs WHERE tenant_id=$1 AND idempotency_key=$2 AND kind=$3",
			tenantID, idemKey, kind))
		if err != nil {
			return err
		}
		j = got
		return nil
	})
	return j, err
}

// CountActive 返回租户下非终态任务数量，用于租户并发上限检查。
func (s *PGStore) CountActive(ctx context.Context, tenantID string) (int, error) {
	count := 0
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM jobs
			 WHERE tenant_id=$1 AND state NOT IN ('succeeded','failed','canceled')`,
			tenantID).Scan(&count)
	})
	return count, err
}

// ByIdempotency 返回某租户同 kind/idempotency_key 的既有任务。
func (s *PGStore) ByIdempotency(ctx context.Context, tenantID, kind, idemKey string) (*Job, error) {
	var j *Job
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		j, e = scanJob(tx.QueryRow(ctx,
			"SELECT "+jobSelectColumns+" FROM jobs WHERE tenant_id=$1 AND kind=$2 AND idempotency_key=$3",
			tenantID, kind, idemKey))
		return e
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrJobNotFound
	}
	return j, err
}

// ClaimNext 以 SKIP LOCKED 领取一个可运行任务。
func (s *PGStore) ClaimNext(ctx context.Context, tenantID, leaseOwner string, leaseFor time.Duration) (*Job, error) {
	var j *Job
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `WITH candidate AS (
			SELECT j.id FROM jobs j
			WHERE j.tenant_id=$1
			  AND (j.run_at IS NULL OR j.run_at <= now())
			  AND (
			    (j.state IN ('queued','retry_wait') AND (j.lease_until IS NULL OR j.lease_until < now()))
			    OR (j.state IN ('running','cancel_requested') AND j.lease_until < now())
			  )
			ORDER BY j.created_at
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE jobs j SET
			state=CASE WHEN j.state='cancel_requested' THEN 'cancel_requested' ELSE 'running' END,
			attempt=j.attempt+1,
			lease_owner=$2, lease_until=now()+$3,
			fencing_token=j.fencing_token+1, updated_at=now()
		FROM candidate c WHERE j.id=c.id
		RETURNING `+jobReturningList("j.", jobColumns),
			tenantID, leaseOwner, leaseFor)
		got, err := scanJob(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoJob
		}
		j = got
		return err
	})
	if err != nil {
		return nil, err
	}
	return j, nil
}

// UpdateProgress 以 fencing 条件更新任务进度（state='running'）并记录事件。
func (s *PGStore) UpdateProgress(ctx context.Context, id, owner string, fencing int64, progress int) error {
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}
	return tenant.RunCtx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			`UPDATE jobs SET progress=$4, updated_at=now()
			 WHERE id=$1 AND lease_owner=$2 AND fencing_token=$3 AND state='running'
			 RETURNING `+jobSelectColumns,
			id, owner, fencing, progress)
		got, err := scanJob(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLeaseMismatch
		}
		if err != nil {
			return err
		}
		return recordJobEvent(ctx, tx, got)
	})
}

// EventsSince 返回某项目在 afterSeq 之后的事件（seq 升序），供 WatchEvents 断点续传。
func (s *PGStore) EventsSince(ctx context.Context, tenantID, projectID string, afterSeq int64, limit int) ([]JobEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var out []JobEvent
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT seq, snapshot FROM job_events
			 WHERE tenant_id=$1 AND project_id=$2 AND seq>$3
			 ORDER BY seq ASC LIMIT $4`,
			tenantID, projectID, afterSeq, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ev JobEvent
			var snapshot []byte
			if err := rows.Scan(&ev.Seq, &snapshot); err != nil {
				return err
			}
			job := &Job{}
			if err := json.Unmarshal(snapshot, job); err != nil {
				return err
			}
			ev.Job = job
			out = append(out, ev)
		}
		return rows.Err()
	})
	return out, err
}

// ClaimNextAny 通过受限调度函数跨租户领取一个可运行任务。
func (s *PGStore) ClaimNextAny(ctx context.Context, leaseOwner string, leaseFor time.Duration) (*Job, error) {
	seconds := int(leaseFor / time.Second)
	if seconds <= 0 {
		seconds = 1
	}
	j, err := scanJob(s.pool.QueryRow(ctx,
		"SELECT "+jobSelectColumns+" FROM ppts_claim_next_job($1,$2)", leaseOwner, seconds))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoJob
	}
	if err != nil {
		return nil, err
	}
	return j, nil
}

// OldestQueuedAge returns the age of the oldest runnable queued job across tenants,
// using the restricted scheduler function. Returns 0 when the queue is empty.
func (s *PGStore) OldestQueuedAge(ctx context.Context) (time.Duration, error) {
	var seconds float64
	err := s.pool.QueryRow(ctx, "SELECT COALESCE(ppts_queue_backlog_seconds(), 0)").Scan(&seconds)
	if err != nil {
		return 0, err
	}
	if seconds <= 0 {
		return 0, nil
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

// Heartbeat 续租；发现取消请求返回 ErrCancelRequested，fencing 不匹配返回 ErrLeaseMismatch。
// 使用 context 中的租户上下文（由 worker 在处理任务前注入）。
func (s *PGStore) Heartbeat(ctx context.Context, id, owner string, fencing int64, extend time.Duration) error {
	return tenant.RunCtx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE jobs SET lease_until=now()+$4, updated_at=now()
			 WHERE id=$1 AND lease_owner=$2 AND fencing_token=$3 AND state='running'`,
			id, owner, fencing, extend)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			return nil
		}
		// 未命中：区分"被请求取消"与"租约失效"。
		var state string
		serr := tx.QueryRow(ctx,
			`SELECT state FROM jobs WHERE id=$1 AND lease_owner=$2 AND fencing_token=$3`,
			id, owner, fencing).Scan(&state)
		if serr == nil && state == string(StateCancelReq) {
			return ErrCancelRequested
		}
		return ErrLeaseMismatch
	})
}

// Complete 以 fencing 条件置为终态或 unknown_provider_result（等待对账）。
func (s *PGStore) Complete(ctx context.Context, id, owner string, fencing int64, state JobState, errMsg []byte) error {
	return tenant.RunCtx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			`UPDATE jobs SET state=$4, lease_owner=NULL, lease_until=NULL,
			   last_error=$5, progress=CASE WHEN $4='succeeded' THEN 100 ELSE progress END,
			   updated_at=now()
			 WHERE id=$1 AND lease_owner=$2 AND fencing_token=$3
			   AND state IN ('running','cancel_requested')
			 RETURNING `+jobSelectColumns,
			id, owner, fencing, string(state), nullableBytes(errMsg))
		got, err := scanJob(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLeaseMismatch
		}
		if err != nil {
			return err
		}
		return recordJobEvent(ctx, tx, got)
	})
}

// CompleteWithStep 以 fencing 条件置为终态，并在同一事务 upsert 最终成功步骤（outbox，G3-5）。
// 步骤与终态原子提交：任一步失败整体回滚，避免"步骤成功但任务未终态"的中间窗口。
func (s *PGStore) CompleteWithStep(ctx context.Context, id, owner string, fencing int64, state JobState, errMsg []byte, step *JobStep) error {
	return tenant.RunCtx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			`UPDATE jobs SET state=$4, lease_owner=NULL, lease_until=NULL,
			   last_error=$5, progress=CASE WHEN $4='succeeded' THEN 100 ELSE progress END,
			   updated_at=now()
			 WHERE id=$1 AND lease_owner=$2 AND fencing_token=$3
			   AND state IN ('running','cancel_requested')
			 RETURNING `+jobSelectColumns,
			id, owner, fencing, string(state), nullableBytes(errMsg))
		got, err := scanJob(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLeaseMismatch
		}
		if err != nil {
			return err
		}
		if step != nil {
			// 与 MarkStep 同一条覆盖规则（steps.go 单一来源）：终态提交携带的最终步骤
			// 是 success，覆盖 pending/failed 不受影响；反之 pending 抹结论同样被拒。
			if _, err := pgStepUpsert(ctx, tx, *step); err != nil {
				return err
			}
		}
		return recordJobEvent(ctx, tx, got)
	})
}

// ScheduleRetry 置 retry_wait 并带退避时间。
func (s *PGStore) ScheduleRetry(ctx context.Context, id, owner string, fencing int64, runAt time.Time, errMsg []byte) error {
	return tenant.RunCtx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			`UPDATE jobs SET state='retry_wait', run_at=$4, lease_owner=NULL, lease_until=NULL,
			   last_error=$5, updated_at=now()
			 WHERE id=$1 AND lease_owner=$2 AND fencing_token=$3 AND state='running'
			 RETURNING `+jobSelectColumns,
			id, owner, fencing, runAt, nullableBytes(errMsg))
		got, err := scanJob(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLeaseMismatch
		}
		if err != nil {
			return err
		}
		return recordJobEvent(ctx, tx, got)
	})
}

// pgStepUpsert 写入/更新步骤行。覆盖条件取自 steps.go 的单一来源（pending 不抹结论）。
//
// 返回受影响行数：0 表示命中唯一冲突但被覆盖条件拒绝（有更强的既有结论），此时**不是错误**。
func pgStepUpsert(ctx context.Context, tx pgx.Tx, step JobStep) (int64, error) {
	tag, err := tx.Exec(ctx,
		`INSERT INTO job_steps (id, job_id, tenant_id, step_type, step_key, state, result_ref)
		 VALUES (gen_random_uuid(), $1,$2,$3,$4,$5,$6)
		 ON CONFLICT (job_id, step_key) DO UPDATE
		   SET state=EXCLUDED.state, result_ref=EXCLUDED.result_ref, updated_at=now()
		   WHERE `+stepOverwriteCond(dialectPG),
		step.JobID, step.TenantID, step.StepType, step.StepKey, string(step.State), step.ResultRef)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// pgAssertLease 校验写入者是否仍持有任务租约（LeaseOwner/FencingToken 任一为零值时不校验）。
//
// 任务终态时 Complete 已把 lease_owner 置 NULL，此时任何带凭据的写入都会被挡下——
// 这正是"任务已 canceled/failed/succeeded 之后不得再改步骤与 phase"的实现方式。
func pgAssertLease(ctx context.Context, tx pgx.Tx, step JobStep) error {
	wantOwner, wantFencing := resolveStepLease(ctx, step)
	if wantOwner == "" && wantFencing == 0 {
		return nil
	}
	var owner *string
	var fencing int64
	err := tx.QueryRow(ctx,
		`SELECT lease_owner, fencing_token FROM jobs WHERE id=$1 AND tenant_id=$2`,
		step.JobID, step.TenantID).Scan(&owner, &fencing)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrJobNotFound
	}
	if err != nil {
		return err
	}
	if owner == nil || *owner != wantOwner || fencing != wantFencing {
		return ErrLeaseMismatch
	}
	return nil
}

// MarkStep 幂等记录步骤状态，并在**同一事务**内维护 jobs.phase（B4-M6b）。
//
// 阶段 = 最近写入的步骤类型。同事务写入是关键：任何已提交的步骤写入必然连带提交阶段，
// 因此不存在「步骤已变、阶段未变」的漂移，jobs.phase 才敢用于列表排序/筛选。
// 刻意**不**更新 jobs.updated_at：本方法原本就不动它（updated_at 供 EventsSince 的增量轮询使用，
// 在此处推进会让轮询把未变更状态的任务误报为已变更）。
//
// 带租约凭据时先校验归属：不匹配（含任务已终态）返回 ErrLeaseMismatch。
func (s *PGStore) MarkStep(ctx context.Context, step JobStep) error {
	return tenant.Run(ctx, s.pool, step.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := pgAssertLease(ctx, tx, step); err != nil {
			return err
		}
		n, err := pgStepUpsert(ctx, tx, step)
		if err != nil {
			return err
		}
		// 覆盖被拒（既有结论更强）= 这次写入对事实没有任何改变，连 phase 也不动：
		// 让"最近写入的步骤类型"保持真实，而不是把一次被忽略的写入记成进度。
		if n == 0 {
			return nil
		}
		// 值未变时不写（避免无谓的行更新与 WAL）；条件里带 tenant_id 以满足 RLS 的写检查。
		_, err = tx.Exec(ctx,
			`UPDATE jobs SET phase=$3
			 WHERE id=$1 AND tenant_id=$2 AND phase <> $3`,
			step.JobID, step.TenantID, step.StepType)
		return err
	})
}

// Cancel 取消任务：queued/retry_wait 直接置 canceled；running 置 cancel_requested
// 由 worker 安全点停止后提交 canceled。
func (s *PGStore) Cancel(ctx context.Context, id, tenantID string) (*Job, error) {
	var j *Job
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		got, err := scanJob(tx.QueryRow(ctx,
			`UPDATE jobs SET
			   state = CASE WHEN state IN ('queued','retry_wait') THEN 'canceled' ELSE 'cancel_requested' END,
			   updated_at=now()
			 WHERE id=$1 AND tenant_id=$2 AND state IN ('queued','retry_wait','running')
			 RETURNING `+jobSelectColumns, id, tenantID))
		if errors.Is(err, pgx.ErrNoRows) {
			// 区分不存在与状态不可取消。
			var state string
			if serr := tx.QueryRow(ctx, `SELECT state FROM jobs WHERE id=$1 AND tenant_id=$2`, id, tenantID).Scan(&state); errors.Is(serr, pgx.ErrNoRows) {
				return ErrJobNotFound
			} else if serr != nil {
				return serr
			}
			return ErrJobNotCancelable
		}
		if err != nil {
			return err
		}
		j = got
		return recordJobEvent(ctx, tx, j)
	})
	if err != nil {
		return nil, err
	}
	return j, nil
}

// RetryFailed 将 failed/unknown_provider_result 任务重新入队（保留原行与 attempt，下次领取 fencing 递增）。
func (s *PGStore) RetryFailed(ctx context.Context, id, tenantID string) (*Job, error) {
	var j *Job
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		got, err := scanJob(tx.QueryRow(ctx,
			`UPDATE jobs SET state='queued', run_at=NULL, lease_owner=NULL, lease_until=NULL, last_error=NULL, updated_at=now()
			 WHERE id=$1 AND tenant_id=$2 AND state IN ('failed','unknown_provider_result')
			 RETURNING `+jobSelectColumns, id, tenantID))
		if errors.Is(err, pgx.ErrNoRows) {
			var state string
			if serr := tx.QueryRow(ctx, `SELECT state FROM jobs WHERE id=$1 AND tenant_id=$2`, id, tenantID).Scan(&state); errors.Is(serr, pgx.ErrNoRows) {
				return ErrJobNotFound
			} else if serr != nil {
				return serr
			}
			return ErrJobNotRetryable
		}
		if err != nil {
			return err
		}
		j = got
		return recordJobEvent(ctx, tx, j)
	})
	if err != nil {
		return nil, err
	}
	return j, nil
}

// List 按项目与状态游标分页（created_at 倒序）；cursor 为空取首页。
func (s *PGStore) List(ctx context.Context, tenantID, projectID, state, cursor string, pageSize int) ([]*Job, string, error) {
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 50
	}
	var out []*Job
	next := ""
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		args := []any{tenantID}
		where := "tenant_id=$1"
		if projectID != "" {
			args = append(args, projectID)
			where += " AND project_id=$" + strconv.Itoa(len(args))
		}
		args = append(args, pageSize+1)
		limit := "$" + strconv.Itoa(len(args))
		if state != "" {
			args = append(args, state)
			where += " AND state=$" + strconv.Itoa(len(args))
		}
		if cursor != "" {
			createdBefore, perr := time.Parse(time.RFC3339Nano, cursor)
			if perr != nil {
				return perr
			}
			args = append(args, createdBefore)
			where += " AND created_at < $" + strconv.Itoa(len(args))
		}
		rows, err := tx.Query(ctx,
			"SELECT "+jobSelectColumns+" FROM jobs WHERE "+where+" ORDER BY created_at DESC LIMIT "+limit, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		out = make([]*Job, 0, pageSize)
		for rows.Next() {
			job, err := scanJob(rows)
			if err != nil {
				return err
			}
			out = append(out, job)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(out) > pageSize {
			next = out[pageSize-1].CreatedAt.Format(time.RFC3339Nano)
			out = out[:pageSize]
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return out, next, nil
}

// UpdatedSince 返回某项目在 after 之后更新的任务，按 updated_at 升序，供 WatchEvents 增量轮询。
func (s *PGStore) UpdatedSince(ctx context.Context, tenantID, projectID string, after time.Time, limit int) ([]*Job, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var out []*Job
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			"SELECT "+jobSelectColumns+" FROM jobs WHERE tenant_id=$1 AND project_id=$2 AND updated_at > $3 ORDER BY updated_at ASC LIMIT $4",
			tenantID, projectID, after, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			job, err := scanJob(rows)
			if err != nil {
				return err
			}
			out = append(out, job)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Get 按 ID + 租户查询。
// Get 按 ID 查任务。
// 缺失时返回包裹了 ErrJobNotFound 的错误：调用方（api.jobError）据此映射 404，
// 而非此前未包裹时被当成内部错误返回 500。
func (s *PGStore) Get(ctx context.Context, id, tenantID string) (*Job, error) {
	var j *Job
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		j, e = scanJob(tx.QueryRow(ctx,
			"SELECT "+jobSelectColumns+" FROM jobs WHERE id=$1 AND tenant_id=$2", id, tenantID))
		return e
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrJobNotFound, id)
	}
	return j, err
}

// GetMany 批量按 ID 查任务（单次查询），供任务列表批量补齐范围/阶段等扩展信息（B4-M6a）。
// 不存在或越权的 ID 不会出现在结果里（不报错，由调用方按 ID 对齐）；ids 为空返回 nil。
func (s *PGStore) GetMany(ctx context.Context, tenantID string, ids []string) ([]*Job, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, tenantID)
	ph := make([]string, 0, len(ids))
	for i, id := range ids {
		ph = append(ph, "$"+strconv.Itoa(i+2))
		args = append(args, id)
	}
	var out []*Job
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			"SELECT "+jobSelectColumns+" FROM jobs WHERE tenant_id=$1 AND id IN ("+strings.Join(ph, ",")+")", args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			j, err := scanJob(rows)
			if err != nil {
				return err
			}
			out = append(out, j)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// LatestSucceededJob 返回某项目最近一次成功的指定类型任务；
// 不存在返回 ErrNoSucceededJob。
func (s *PGStore) LatestSucceededJob(ctx context.Context, tenantID, projectID, kind string) (*Job, error) {
	var j *Job
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		j, e = scanJob(tx.QueryRow(ctx,
			"SELECT "+jobSelectColumns+" FROM jobs WHERE tenant_id=$1 AND project_id=$2 AND kind=$3 AND state='succeeded' ORDER BY created_at DESC LIMIT 1",
			tenantID, projectID, kind))
		return e
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoSucceededJob
	}
	return j, err
}

// StepResultRef 返回某任务最近成功的指定步骤的 result_ref；
// 无记录时不返回错误（ref 为空，由调用方决定 NotFound）。
// 使用 context 中的租户上下文（调用方应已从身份取得租户）。
func (s *PGStore) StepResultRef(ctx context.Context, jobID, stepType string) (string, error) {
	var ref string
	err := tenant.RunCtx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		serr := tx.QueryRow(ctx,
			`SELECT result_ref FROM job_steps
			 WHERE job_id=$1 AND step_type=$2 AND state='success'
			 ORDER BY updated_at DESC LIMIT 1`, jobID, stepType).Scan(&ref)
		if errors.Is(serr, pgx.ErrNoRows) {
			return nil
		}
		return serr
	})
	if err != nil {
		return "", err
	}
	return ref, nil
}

// ListSteps 返回某任务的全部步骤，按更新时间升序（同刻按 step_type/step_key 稳定排序），
// 供任务详情展示执行步骤（B4-M6a）。此前 job_steps 只有写入路径（MarkStep）与内部按 step_key
// 取 result_ref，没有任何对外读取。
// 注意：result_ref 是内部对象键，调用方不应直接透出给最终用户（api 层只回 hasResult 布尔）。
func (s *PGStore) ListSteps(ctx context.Context, tenantID, jobID string) ([]JobStep, error) {
	var out []JobStep
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id, job_id, tenant_id, step_type, step_key, state, result_ref, updated_at
			   FROM job_steps
			  WHERE job_id=$1 AND tenant_id=$2
			  ORDER BY updated_at, step_type, step_key`, jobID, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var st JobStep
			if err := rows.Scan(&st.ID, &st.JobID, &st.TenantID, &st.StepType, &st.StepKey,
				&st.State, &st.ResultRef, &st.UpdatedAt); err != nil {
				return err
			}
			out = append(out, st)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// mustJSONArray 把字符串切片序列化成 jsonb 参数文本（nil / 序列化失败退化为空数组）。
// affected_pages 只是可查询投影，其解析失败不应让任务创建失败。
func mustJSONArray(items []string) string {
	if items == nil {
		return "[]"
	}
	b, err := json.Marshal(items)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// jobCursorSep 分隔「排序键值」与「任务 id」；用不可见字符避免与时间戳/阶段名冲突。
const jobCursorSep = "\x1f"

func encodeJobCursor(sortValue, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(sortValue + jobCursorSep + id))
}

func decodeJobCursor(cursor string) (sortValue, id string, err error) {
	raw, derr := base64.RawURLEncoding.DecodeString(cursor)
	if derr != nil {
		return "", "", ErrBadJobCursor
	}
	parts := strings.SplitN(string(raw), jobCursorSep, 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", ErrBadJobCursor
	}
	return parts[0], parts[1], nil
}

// jobPageSortValue 取一行在当前排序键下的值（用于生成下一页游标）。
func jobPageSortValue(f JobFilter, row JobPageRow) string {
	switch f.Sort {
	case "updated":
		return row.Job.UpdatedAt.Format(time.RFC3339Nano)
	case "phase":
		return row.Phase
	case "pages":
		return strconv.Itoa(row.PageCount)
	default:
		return row.Job.CreatedAt.Format(time.RFC3339Nano)
	}
}

// ListPage 按筛选/排序分页返回任务（B4-M6b），供控制台任务列表的「阶段筛选 + 排序 + 翻页」。
//
// 游标是 (排序键值, id) 的 keyset（base64）而非 offset：并发插入/删除不会造成翻页重复或漏项；
// 排序键与 id 一同参与比较，保证排序键相同的多行之间也有确定顺序，翻页不会卡在同一处。
func (s *PGStore) ListPage(ctx context.Context, tenantID string, f JobFilter, cursor string, pageSize int) ([]JobPageRow, string, error) {
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	sortExpr, sortCast := jobSortSpec(dialectPG, f.Sort)
	dir, cmp := "DESC", "<"
	if !f.Desc {
		dir, cmp = "ASC", ">"
	}
	var out []JobPageRow
	next := ""
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		out, next = nil, ""
		args := []any{tenantID}
		where := "tenant_id=$1"
		if f.ProjectID != "" {
			args = append(args, f.ProjectID)
			where += " AND project_id=$" + strconv.Itoa(len(args))
		}
		if f.Phase != "" {
			args = append(args, f.Phase)
			where += " AND phase=$" + strconv.Itoa(len(args))
		}
		if cursor != "" {
			key, id, cerr := decodeJobCursor(cursor)
			if cerr != nil {
				return cerr
			}
			args = append(args, key)
			keyPH := "$" + strconv.Itoa(len(args))
			args = append(args, id)
			idPH := "$" + strconv.Itoa(len(args))
			// 行值比较 (sortExpr, id) </> (key, id)：两侧类型必须显式转换。
			where += fmt.Sprintf(" AND (%s, id) %s (%s::%s, %s::uuid)", sortExpr, cmp, keyPH, sortCast, idPH)
		}
		args = append(args, pageSize+1)
		limitPH := "$" + strconv.Itoa(len(args))
		rows, err := tx.Query(ctx,
			"SELECT "+jobSelectColumns+jobPageExtraColumns(dialectPG)+" FROM jobs WHERE "+where+
				" ORDER BY "+sortExpr+" "+dir+", id "+dir+" LIMIT "+limitPH, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		out = make([]JobPageRow, 0, pageSize)
		for rows.Next() {
			row, err := scanJobPageRow(rows)
			if err != nil {
				return err
			}
			out = append(out, row)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(out) > pageSize {
			last := out[pageSize-1]
			next = encodeJobCursor(jobPageSortValue(f, last), last.Job.ID)
			out = out[:pageSize]
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return out, next, nil
}

// PhaseCounts 返回各阶段的任务数（可按项目过滤），供列表的阶段筛选项给出可选值与计数。
// 尚无任何步骤的任务阶段为空串，以空串为键返回，由调用方决定如何展示（不在此处伪造成某个阶段）。
func (s *PGStore) PhaseCounts(ctx context.Context, tenantID, projectID string) (map[string]int, error) {
	out := map[string]int{}
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		args := []any{tenantID}
		where := "tenant_id=$1"
		if projectID != "" {
			args = append(args, projectID)
			where += " AND project_id=$" + strconv.Itoa(len(args))
		}
		rows, err := tx.Query(ctx, `SELECT phase, count(*) FROM jobs WHERE `+where+` GROUP BY phase`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var phase string
			var n int
			if err := rows.Scan(&phase, &n); err != nil {
				return err
			}
			out[phase] = n
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}

func nullableBytes(b []byte) any {
	if b == nil {
		return nil
	}
	return b
}

// recordJobEvent 在事务内写入任务事件（WatchEvents 单调 seq，G3-9）。
// 事务必须已处于租户上下文（tenant.Run/RunCtx）。
func recordJobEvent(ctx context.Context, tx pgx.Tx, job *Job) error {
	if job == nil {
		return nil
	}
	snapshot, err := json.Marshal(job)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO job_events (tenant_id, project_id, job_id, state, progress, snapshot)
		 VALUES ($1,$2,$3,$4,$5,$6)`,
		job.TenantID, job.ProjectID, job.ID, string(job.State), job.Progress, snapshot)
	return err
}
