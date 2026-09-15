package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
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

const jobSelectColumns = `id, tenant_id, project_id, kind, state, input_snapshot,
	idempotency_key, attempt, lease_owner, lease_until, fencing_token,
	run_at, progress, last_error, created_at, updated_at, traceparent`

func scanJob(row pgx.Row) (*Job, error) {
	var j Job
	var createdAt, updatedAt time.Time
	var leaseOwner *string
	var runAtPtr, leaseUntilPtr *time.Time
	var lastErr []byte
	err := row.Scan(&j.ID, &j.TenantID, &j.ProjectID, &j.Kind, &j.State,
		&j.InputSnapshot, &j.IDempotencyKey, &j.Attempt, &leaseOwner,
		&leaseUntilPtr, &j.FencingToken, &runAtPtr, &j.Progress, &lastErr,
		&createdAt, &updatedAt, &j.TraceParent)
	if err != nil {
		return nil, err
	}
	if leaseOwner != nil {
		j.LeaseOwner = *leaseOwner
	}
	if leaseUntilPtr != nil {
		j.LeaseUntil = *leaseUntilPtr
	}
	if runAtPtr != nil {
		j.RunAt = *runAtPtr
	}
	j.CreatedAt, j.UpdatedAt = createdAt, updatedAt
	if len(lastErr) > 0 {
		j.LastError = &JobError{}
		_ = json.Unmarshal(lastErr, j.LastError)
	}
	return &j, nil
}

// Create idempotent：同 (tenant_id, idempotency_key, kind) 命中唯一约束时返回既有行。
func (s *PGStore) Create(ctx context.Context, tenantID, projectID, kind, idemKey, snapshot string, runAt time.Time) (*Job, error) {
	var j *Job
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `INSERT INTO jobs
			(id, tenant_id, project_id, kind, state, idempotency_key, input_snapshot, run_at, traceparent)
			VALUES (gen_random_uuid(),$1,$2,$3,'queued',$4,$5,$6,$7)
			ON CONFLICT (tenant_id, idempotency_key, kind) DO NOTHING
			RETURNING `+jobSelectColumns,
			tenantID, projectID, kind, idemKey, snapshot, nullableTime(&runAt), traceprop.FromContext(ctx))
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
		RETURNING j.id, j.tenant_id, j.project_id, j.kind, j.state, j.input_snapshot,
		          j.idempotency_key, j.attempt, j.lease_owner, j.lease_until,
		          j.fencing_token, j.run_at, j.progress, j.last_error, j.created_at, j.updated_at, j.traceparent`,
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
			_, err := tx.Exec(ctx,
				`INSERT INTO job_steps (id, job_id, tenant_id, step_type, step_key, state, result_ref)
				 VALUES (gen_random_uuid(), $1,$2,$3,$4,$5,$6)
				 ON CONFLICT (job_id, step_key) DO UPDATE
				   SET state=EXCLUDED.state, result_ref=EXCLUDED.result_ref, updated_at=now()`,
				step.JobID, step.TenantID, step.StepType, step.StepKey, string(step.State), step.ResultRef)
			if err != nil {
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

// MarkStep 幂等记录步骤状态。
func (s *PGStore) MarkStep(ctx context.Context, step JobStep) error {
	return tenant.Run(ctx, s.pool, step.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO job_steps (id, job_id, tenant_id, step_type, step_key, state, result_ref)
			 VALUES (gen_random_uuid(), $1,$2,$3,$4,$5,$6)
			 ON CONFLICT (job_id, step_key) DO UPDATE
			   SET state=EXCLUDED.state, result_ref=EXCLUDED.result_ref, updated_at=now()`,
			step.JobID, step.TenantID, step.StepType, step.StepKey, string(step.State), step.ResultRef)
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
func (s *PGStore) Get(ctx context.Context, id, tenantID string) (*Job, error) {
	var j *Job
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		j, e = scanJob(tx.QueryRow(ctx,
			"SELECT "+jobSelectColumns+" FROM jobs WHERE id=$1 AND tenant_id=$2", id, tenantID))
		return e
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("pipeline: job %s not found", id)
	}
	return j, err
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
