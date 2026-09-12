package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore 以 PostgreSQL 实现 Store（V4.0 §10.2）：
// SKIP LOCKED 领取 + 原子租约/fencing + 事务外执行 + fencing 条件提交。
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
	run_at, progress, last_error, created_at, updated_at`

func scanJob(row pgx.Row) (*Job, error) {
	var j Job
	var createdAt, updatedAt time.Time
	var leaseOwner *string
	var runAtPtr, leaseUntilPtr *time.Time
	var lastErr []byte
	err := row.Scan(&j.ID, &j.TenantID, &j.ProjectID, &j.Kind, &j.State,
		&j.InputSnapshot, &j.IDempotencyKey, &j.Attempt, &leaseOwner,
		&leaseUntilPtr, &j.FencingToken, &runAtPtr, &j.Progress, &lastErr,
		&createdAt, &updatedAt)
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
	row := s.pool.QueryRow(ctx, `INSERT INTO jobs
		(id, tenant_id, project_id, kind, state, idempotency_key, input_snapshot, run_at)
		VALUES (gen_random_uuid(),$1,$2,$3,'queued',$4,$5,$6)
		ON CONFLICT (tenant_id, idempotency_key, kind) DO NOTHING
		RETURNING `+jobSelectColumns,
		tenantID, projectID, kind, idemKey, snapshot, nullableTime(&runAt))
	j, err := scanJob(row)
	if err == nil {
		return j, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	// 命中幂等约束：返回既有行。
	existing, err := s.lookup(ctx, tenantID, idemKey, kind)
	if err != nil {
		return nil, err
	}
	return existing, nil
}

func (s *PGStore) lookup(ctx context.Context, tenantID, idemKey, kind string) (*Job, error) {
	return scanJob(s.pool.QueryRow(ctx,
		"SELECT "+jobSelectColumns+" FROM jobs WHERE tenant_id=$1 AND idempotency_key=$2 AND kind=$3",
		tenantID, idemKey, kind))
}

// ClaimNext 以 SKIP LOCKED 领取一个可运行任务。
func (s *PGStore) ClaimNext(ctx context.Context, tenantID, leaseOwner string, leaseFor time.Duration) (*Job, error) {
	row := s.pool.QueryRow(ctx, `WITH candidate AS (
		SELECT j.id FROM jobs j
		WHERE j.tenant_id=$1
		  AND (j.run_at IS NULL OR j.run_at <= now())
		  AND (
		    (j.state IN ('queued','retry_wait') AND (j.lease_until IS NULL OR j.lease_until < now()))
		    OR (j.state='running' AND j.lease_until < now()) -- 崩溃 worker 租约过期可重领取
		  )
		ORDER BY j.created_at
		LIMIT 1
		FOR UPDATE SKIP LOCKED
	)
	UPDATE jobs j SET
		state='running', attempt=j.attempt+1,
		lease_owner=$2, lease_until=now()+$3,
		fencing_token=j.fencing_token+1, updated_at=now()
	FROM candidate c WHERE j.id=c.id
	RETURNING j.id, j.tenant_id, j.project_id, j.kind, j.state, j.input_snapshot,
	          j.idempotency_key, j.attempt, j.lease_owner, j.lease_until,
	          j.fencing_token, j.run_at, j.progress, j.last_error, j.created_at, j.updated_at`,
		tenantID, leaseOwner, leaseFor)
	j, err := scanJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoJob
	}
	return j, err
}

// Heartbeat 续租；fencing 不匹配或非运行态返回 ErrLeaseMismatch。
func (s *PGStore) Heartbeat(ctx context.Context, id, owner string, fencing int64, extend time.Duration) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE jobs SET lease_until=now()+$4, updated_at=now()
		 WHERE id=$1 AND lease_owner=$2 AND fencing_token=$3 AND state='running'`,
		id, owner, fencing, extend)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseMismatch
	}
	return nil
}

// Complete 以 fencing 条件置为终态。
func (s *PGStore) Complete(ctx context.Context, id, owner string, fencing int64, state JobState, errMsg []byte) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE jobs SET state=$4, lease_owner=NULL, lease_until=NULL,
		   last_error=$5, progress=CASE WHEN $4='succeeded' THEN 100 ELSE progress END,
		   updated_at=now()
		 WHERE id=$1 AND lease_owner=$2 AND fencing_token=$3 AND state='running'`,
		id, owner, fencing, string(state), nullableBytes(errMsg))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseMismatch
	}
	return nil
}

// ScheduleRetry 置 retry_wait 并带退避时间。
func (s *PGStore) ScheduleRetry(ctx context.Context, id, owner string, fencing int64, runAt time.Time, errMsg []byte) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE jobs SET state='retry_wait', run_at=$4, lease_owner=NULL, lease_until=NULL,
		   last_error=$5, updated_at=now()
		 WHERE id=$1 AND lease_owner=$2 AND fencing_token=$3 AND state='running'`,
		id, owner, fencing, runAt, nullableBytes(errMsg))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseMismatch
	}
	return nil
}

// MarkStep 幂等记录步骤状态。
func (s *PGStore) MarkStep(ctx context.Context, step JobStep) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO job_steps (id, job_id, tenant_id, step_type, step_key, state, result_ref)
		 VALUES (gen_random_uuid(), $1,$2,$3,$4,$5,$6)
		 ON CONFLICT (job_id, step_key) DO UPDATE
		   SET state=EXCLUDED.state, result_ref=EXCLUDED.result_ref, updated_at=now()`,
		step.JobID, step.TenantID, step.StepType, step.StepKey, string(step.State), step.ResultRef)
	return err
}

// CancelRequested 客户端先持久化取消请求；worker 在安全点检查。
func (s *PGStore) CancelRequested(ctx context.Context, id, tenantID string) (*Job, error) {
	return scanJob(s.pool.QueryRow(ctx,
		`UPDATE jobs SET state='cancel_requested', updated_at=now()
		 WHERE id=$1 AND tenant_id=$2
		   AND state IN ('queued','running','retry_wait')
		 RETURNING `+jobSelectColumns, id, tenantID))
}

// Get 按 ID + 租户查询。
func (s *PGStore) Get(ctx context.Context, id, tenantID string) (*Job, error) {
	j, err := scanJob(s.pool.QueryRow(ctx,
		"SELECT "+jobSelectColumns+" FROM jobs WHERE id=$1 AND tenant_id=$2", id, tenantID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("pipeline: job %s not found", id)
	}
	return j, err
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
