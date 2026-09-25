package pipeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/internal/traceprop"
)

// SQLiteStore 是 Store（及调度辅助方法）的 SQLite 实现（单租户精简 profile）。
//
// 与 PGStore 的差异：
//   - 无 SKIP LOCKED：SQLite 单写者，事务本身串行化领取（候选 SELECT + 条件 UPDATE 在同一事务内）；
//   - 无 SECURITY DEFINER 跨租户函数：ClaimNextAny 退化为本地租户 ClaimNext；
//   - 无 BACKLOG 函数：OldestQueuedAge 直接查最早可运行任务；
//   - 时间列为 RFC3339 TEXT，比较依赖同格式的字典序（等价时间序）。
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore 创建 SQLite 任务存储。
func NewSQLiteStore(sqldb *sql.DB) *SQLiteStore { return &SQLiteStore{db: sqldb} }

// Close 无连接池需要关闭（由 db 包统一管理），保留以对齐 PGStore 的调用形态。
func (s *SQLiteStore) Close() {}

var _ Store = (*SQLiteStore)(nil)

type sqJobBuf struct {
	j          Job
	createdAt  string
	updatedAt  string
	leaseOwner sql.NullString
	runAt      sql.NullString
	leaseUntil sql.NullString
	lastErr    []byte
}

// dest 返回与 jobSelectColumns 严格同序的扫描目标；调用方可在其后追加额外列。
// 顺序来自 columns.go 的 jobColumns 表（与 SELECT 文本同源），此处不再手写第二份。
func (b *sqJobBuf) dest() []any {
	return sqJobDest(jobColumns, b)
}

func (b *sqJobBuf) job() *Job {
	if b.leaseOwner.Valid {
		b.j.LeaseOwner = b.leaseOwner.String
	}
	if b.leaseUntil.Valid {
		b.j.LeaseUntil = db.ParseTime(b.leaseUntil.String)
	}
	if b.runAt.Valid {
		b.j.RunAt = db.ParseTime(b.runAt.String)
	}
	b.j.CreatedAt = db.ParseTime(b.createdAt)
	b.j.UpdatedAt = db.ParseTime(b.updatedAt)
	if len(b.lastErr) > 0 {
		b.j.LastError = &JobError{}
		_ = json.Unmarshal(b.lastErr, b.j.LastError)
	}
	return &b.j
}

type sqRowScanner interface{ Scan(dest ...any) error }

func sqScanJob(row sqRowScanner) (*Job, error) {
	b := &sqJobBuf{}
	if err := row.Scan(b.dest()...); err != nil {
		return nil, err
	}
	return b.job(), nil
}

func sqScanJobPageRow(row sqRowScanner) (JobPageRow, error) {
	b := &sqJobBuf{}
	var phase string
	var pageCount int
	if err := row.Scan(append(b.dest(), &phase, &pageCount)...); err != nil {
		return JobPageRow{}, err
	}
	return JobPageRow{Job: b.job(), Phase: phase, PageCount: pageCount}, nil
}

// sqTxQueryer 统一 *sql.DB 与 *sql.Tx。
type sqTxQueryer interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
}

// recordJobEvent 写入任务事件（快照 JSON），返回无错即表示成功。
func sqRecordJobEvent(ctx context.Context, q sqTxQueryer, job *Job) error {
	if job == nil {
		return nil
	}
	snapshot, err := json.Marshal(job)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx,
		`INSERT INTO job_events (tenant_id, project_id, job_id, state, progress, snapshot)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		job.TenantID, job.ProjectID, job.ID, string(job.State), job.Progress, string(snapshot))
	return err
}

func sqNow() string { return db.Now() }

func sqNullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return db.FormatTime(t)
}

func (s *SQLiteStore) Create(ctx context.Context, tenantID, projectID, kind, idemKey, snapshot string, runAt time.Time) (*Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	id := uuid.New().String()
	now := sqNow()
	_, err = tx.ExecContext(ctx,
		`INSERT INTO jobs (id, tenant_id, project_id, kind, state, idempotency_key,
		   input_snapshot, run_at, traceparent, affected_pages, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'queued', ?, ?, ?, ?, ?, ?, ?)`,
		id, tenantID, projectID, kind, idemKey, snapshot, sqNullableTime(runAt),
		traceprop.FromContext(ctx), mustJSONArray(AffectedPagesOf(JobKind(kind), snapshot)), now, now)
	if err != nil {
		if sqIsUnique(err) {
			// 命中幂等约束：返回既有行，不重复写事件。
			j, qerr := sqScanJob(tx.QueryRowContext(ctx,
				`SELECT `+jobSelectColumns+` FROM jobs WHERE tenant_id = ? AND idempotency_key = ? AND kind = ?`,
				tenantID, idemKey, kind))
			if qerr != nil {
				return nil, qerr
			}
			return j, tx.Commit()
		}
		return nil, err
	}
	j, err := sqScanJob(tx.QueryRowContext(ctx, `SELECT `+jobSelectColumns+` FROM jobs WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	if err := sqRecordJobEvent(ctx, tx, j); err != nil {
		return nil, err
	}
	return j, tx.Commit()
}

func (s *SQLiteStore) CountActive(ctx context.Context, tenantID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM jobs WHERE tenant_id = ? AND state NOT IN ('succeeded','failed','canceled')`,
		tenantID).Scan(&n)
	return n, err
}

func (s *SQLiteStore) ByIdempotency(ctx context.Context, tenantID, kind, idemKey string) (*Job, error) {
	j, err := sqScanJob(s.db.QueryRowContext(ctx,
		`SELECT `+jobSelectColumns+` FROM jobs WHERE tenant_id = ? AND kind = ? AND idempotency_key = ?`,
		tenantID, kind, idemKey))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrJobNotFound
	}
	return j, err
}

func (s *SQLiteStore) ClaimNext(ctx context.Context, tenantID, leaseOwner string, leaseFor time.Duration) (*Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	now := sqNow()
	var id string
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM jobs
		 WHERE tenant_id = ?
		   AND (run_at IS NULL OR run_at <= ?)
		   AND (
		     (state IN ('queued','retry_wait') AND (lease_until IS NULL OR lease_until < ?))
		     OR (state IN ('running','cancel_requested') AND lease_until < ?))
		 ORDER BY created_at LIMIT 1`,
		tenantID, now, now, now).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoJob
	}
	if err != nil {
		return nil, err
	}
	leaseUntil := db.FormatTime(time.Now().Add(leaseFor))
	if _, err := tx.ExecContext(ctx,
		`UPDATE jobs SET
		   state = CASE WHEN state='cancel_requested' THEN 'cancel_requested' ELSE 'running' END,
		   attempt = attempt + 1,
		   lease_owner = ?, lease_until = ?,
		   fencing_token = fencing_token + 1, updated_at = ?
		 WHERE id = ?`, leaseOwner, leaseUntil, now, id); err != nil {
		return nil, err
	}
	j, err := sqScanJob(tx.QueryRowContext(ctx, `SELECT `+jobSelectColumns+` FROM jobs WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	return j, tx.Commit()
}

func (s *SQLiteStore) ClaimNextAny(ctx context.Context, leaseOwner string, leaseFor time.Duration) (*Job, error) {
	// 单租户：直接领取本地租户任务（无跨租户概念）。
	var tenantID string
	if err := s.db.QueryRowContext(ctx,
		`SELECT id FROM tenants ORDER BY created_at LIMIT 1`).Scan(&tenantID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoJob
		}
		return nil, err
	}
	return s.ClaimNext(ctx, tenantID, leaseOwner, leaseFor)
}

func (s *SQLiteStore) OldestQueuedAge(ctx context.Context) (time.Duration, error) {
	var created sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT MIN(created_at) FROM jobs WHERE state = 'queued'`).Scan(&created)
	if err != nil || !created.Valid {
		return 0, err
	}
	age := time.Since(db.ParseTime(created.String))
	if age < 0 {
		return 0, nil
	}
	return age, nil
}

func (s *SQLiteStore) Heartbeat(ctx context.Context, id, owner string, fencing int64, extend time.Duration) error {
	leaseUntil := db.FormatTime(time.Now().Add(extend))
	res, err := s.db.ExecContext(ctx,
		`UPDATE jobs SET lease_until = ?, updated_at = ?
		 WHERE id = ? AND lease_owner = ? AND fencing_token = ? AND state = 'running'`,
		leaseUntil, sqNow(), id, owner, fencing)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	var state string
	serr := s.db.QueryRowContext(ctx,
		`SELECT state FROM jobs WHERE id = ? AND lease_owner = ? AND fencing_token = ?`,
		id, owner, fencing).Scan(&state)
	if serr == nil && state == string(StateCancelReq) {
		return ErrCancelRequested
	}
	return ErrLeaseMismatch
}

func (s *SQLiteStore) Complete(ctx context.Context, id, owner string, fencing int64, state JobState, errMsg []byte) error {
	return s.sqComplete(ctx, id, owner, fencing, state, errMsg, nil)
}

func (s *SQLiteStore) CompleteWithStep(ctx context.Context, id, owner string, fencing int64, state JobState, errMsg []byte, step *JobStep) error {
	return s.sqComplete(ctx, id, owner, fencing, state, errMsg, step)
}

func (s *SQLiteStore) sqComplete(ctx context.Context, id, owner string, fencing int64, state JobState, errMsg []byte, step *JobStep) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	progress := 0
	if state == StateSucceeded {
		progress = 100
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE jobs SET state = ?, lease_owner = NULL, lease_until = NULL,
		   last_error = ?, progress = CASE WHEN ? = 'succeeded' THEN 100 ELSE progress END,
		   updated_at = ?
		 WHERE id = ? AND lease_owner = ? AND fencing_token = ?
		   AND state IN ('running','cancel_requested')`,
		string(state), nullableBytes(errMsg), string(state), sqNow(), id, owner, fencing)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrLeaseMismatch
	}
	if step != nil {
		if err := sqUpsertStep(ctx, tx, *step); err != nil {
			return err
		}
	}
	j, err := sqScanJob(tx.QueryRowContext(ctx, `SELECT `+jobSelectColumns+` FROM jobs WHERE id = ?`, id))
	if err != nil {
		return err
	}
	_ = progress
	if err := sqRecordJobEvent(ctx, tx, j); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) ScheduleRetry(ctx context.Context, id, owner string, fencing int64, runAt time.Time, errMsg []byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx,
		`UPDATE jobs SET state = 'retry_wait', run_at = ?, lease_owner = NULL, lease_until = NULL,
		   last_error = ?, updated_at = ?
		 WHERE id = ? AND lease_owner = ? AND fencing_token = ? AND state = 'running'`,
		db.FormatTime(runAt), nullableBytes(errMsg), sqNow(), id, owner, fencing)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrLeaseMismatch
	}
	j, err := sqScanJob(tx.QueryRowContext(ctx, `SELECT `+jobSelectColumns+` FROM jobs WHERE id = ?`, id))
	if err != nil {
		return err
	}
	if err := sqRecordJobEvent(ctx, tx, j); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) MarkStep(ctx context.Context, step JobStep) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := sqUpsertStep(ctx, tx, step); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE jobs SET phase = ? WHERE id = ? AND tenant_id = ? AND phase <> ?`,
		step.StepType, step.JobID, step.TenantID, step.StepType); err != nil {
		return err
	}
	return tx.Commit()
}

func sqUpsertStep(ctx context.Context, q sqTxQueryer, step JobStep) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO job_steps (id, job_id, tenant_id, step_type, step_key, state, result_ref, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(job_id, step_key) DO UPDATE
		   SET state = excluded.state, result_ref = excluded.result_ref, updated_at = excluded.updated_at`,
		uuid.New().String(), step.JobID, step.TenantID, step.StepType, step.StepKey,
		string(step.State), step.ResultRef, sqNow())
	return err
}

func (s *SQLiteStore) UpdateProgress(ctx context.Context, id, owner string, fencing int64, progress int) error {
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx,
		`UPDATE jobs SET progress = ?, updated_at = ?
		 WHERE id = ? AND lease_owner = ? AND fencing_token = ? AND state = 'running'`,
		progress, sqNow(), id, owner, fencing)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrLeaseMismatch
	}
	j, err := sqScanJob(tx.QueryRowContext(ctx, `SELECT `+jobSelectColumns+` FROM jobs WHERE id = ?`, id))
	if err != nil {
		return err
	}
	if err := sqRecordJobEvent(ctx, tx, j); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) EventsSince(ctx context.Context, tenantID, projectID string, afterSeq int64, limit int) ([]JobEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, snapshot FROM job_events
		 WHERE tenant_id = ? AND project_id = ? AND seq > ?
		 ORDER BY seq ASC LIMIT ?`, tenantID, projectID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JobEvent
	for rows.Next() {
		var ev JobEvent
		var snapshot string
		if err := rows.Scan(&ev.Seq, &snapshot); err != nil {
			return nil, err
		}
		job := &Job{}
		if err := json.Unmarshal([]byte(snapshot), job); err != nil {
			return nil, err
		}
		ev.Job = job
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) Cancel(ctx context.Context, id, tenantID string) (*Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx,
		`UPDATE jobs SET
		   state = CASE WHEN state IN ('queued','retry_wait') THEN 'canceled' ELSE 'cancel_requested' END,
		   updated_at = ?
		 WHERE id = ? AND tenant_id = ? AND state IN ('queued','retry_wait','running')`,
		sqNow(), id, tenantID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var state string
		if serr := tx.QueryRowContext(ctx, `SELECT state FROM jobs WHERE id = ? AND tenant_id = ?`, id, tenantID).Scan(&state); errors.Is(serr, sql.ErrNoRows) {
			return nil, ErrJobNotFound
		} else if serr != nil {
			return nil, serr
		}
		return nil, ErrJobNotCancelable
	}
	j, err := sqScanJob(tx.QueryRowContext(ctx, `SELECT `+jobSelectColumns+` FROM jobs WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	if err := sqRecordJobEvent(ctx, tx, j); err != nil {
		return nil, err
	}
	return j, tx.Commit()
}

func (s *SQLiteStore) RetryFailed(ctx context.Context, id, tenantID string) (*Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx,
		`UPDATE jobs SET state = 'queued', run_at = NULL, lease_owner = NULL, lease_until = NULL,
		   last_error = NULL, updated_at = ?
		 WHERE id = ? AND tenant_id = ? AND state IN ('failed','unknown_provider_result')`,
		sqNow(), id, tenantID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var state string
		if serr := tx.QueryRowContext(ctx, `SELECT state FROM jobs WHERE id = ? AND tenant_id = ?`, id, tenantID).Scan(&state); errors.Is(serr, sql.ErrNoRows) {
			return nil, ErrJobNotFound
		} else if serr != nil {
			return nil, serr
		}
		return nil, ErrJobNotRetryable
	}
	j, err := sqScanJob(tx.QueryRowContext(ctx, `SELECT `+jobSelectColumns+` FROM jobs WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	if err := sqRecordJobEvent(ctx, tx, j); err != nil {
		return nil, err
	}
	return j, tx.Commit()
}

func (s *SQLiteStore) List(ctx context.Context, tenantID, projectID, state, cursor string, pageSize int) ([]*Job, string, error) {
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 50
	}
	args := []any{tenantID}
	where := "tenant_id = ?"
	if projectID != "" {
		where += " AND project_id = ?"
		args = append(args, projectID)
	}
	if state != "" {
		where += " AND state = ?"
		args = append(args, state)
	}
	if cursor != "" {
		where += " AND created_at < ?"
		args = append(args, db.FormatTime(db.ParseTime(cursor)))
	}
	args = append(args, pageSize+1)
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+jobSelectColumns+` FROM jobs WHERE `+where+` ORDER BY created_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := make([]*Job, 0, pageSize)
	for rows.Next() {
		j, err := sqScanJob(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > pageSize {
		next = out[pageSize-1].CreatedAt.Format(time.RFC3339Nano)
		out = out[:pageSize]
	}
	return out, next, nil
}

func (s *SQLiteStore) UpdatedSince(ctx context.Context, tenantID, projectID string, after time.Time, limit int) ([]*Job, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+jobSelectColumns+` FROM jobs
		 WHERE tenant_id = ? AND project_id = ? AND updated_at > ?
		 ORDER BY updated_at ASC LIMIT ?`,
		tenantID, projectID, db.FormatTime(after), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Job
	for rows.Next() {
		j, err := sqScanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) Get(ctx context.Context, id, tenantID string) (*Job, error) {
	j, err := sqScanJob(s.db.QueryRowContext(ctx,
		`SELECT `+jobSelectColumns+` FROM jobs WHERE id = ? AND tenant_id = ?`, id, tenantID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrJobNotFound, id)
	}
	return j, err
}

func (s *SQLiteStore) GetMany(ctx context.Context, tenantID string, ids []string) ([]*Job, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := []any{tenantID}
	ph := make([]string, 0, len(ids))
	for _, id := range ids {
		ph = append(ph, "?")
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+jobSelectColumns+` FROM jobs WHERE tenant_id = ? AND id IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Job
	for rows.Next() {
		j, err := sqScanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) LatestSucceededJob(ctx context.Context, tenantID, projectID, kind string) (*Job, error) {
	j, err := sqScanJob(s.db.QueryRowContext(ctx,
		`SELECT `+jobSelectColumns+` FROM jobs
		 WHERE tenant_id = ? AND project_id = ? AND kind = ? AND state = 'succeeded'
		 ORDER BY created_at DESC LIMIT 1`, tenantID, projectID, kind))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoSucceededJob
	}
	return j, err
}

func (s *SQLiteStore) StepResultRef(ctx context.Context, jobID, stepType string) (string, error) {
	var ref string
	err := s.db.QueryRowContext(ctx,
		`SELECT result_ref FROM job_steps
		 WHERE job_id = ? AND step_type = ? AND state = 'success'
		 ORDER BY updated_at DESC LIMIT 1`, jobID, stepType).Scan(&ref)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return ref, nil
}

func (s *SQLiteStore) ListSteps(ctx context.Context, tenantID, jobID string) ([]JobStep, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, job_id, tenant_id, step_type, step_key, state, result_ref, updated_at
		   FROM job_steps WHERE job_id = ? AND tenant_id = ?
		  ORDER BY updated_at, step_type, step_key`, jobID, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JobStep
	for rows.Next() {
		var st JobStep
		var updated string
		if err := rows.Scan(&st.ID, &st.JobID, &st.TenantID, &st.StepType, &st.StepKey,
			&st.State, &st.ResultRef, &updated); err != nil {
			return nil, err
		}
		st.UpdatedAt = db.ParseTime(updated)
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) ListPage(ctx context.Context, tenantID string, f JobFilter, cursor string, pageSize int) ([]JobPageRow, string, error) {
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	sortExpr, _ := jobSortSpec(dialectSQLite, f.Sort)
	dir, cmp := "DESC", "<"
	if !f.Desc {
		dir, cmp = "ASC", ">"
	}
	args := []any{tenantID}
	where := "tenant_id = ?"
	if f.ProjectID != "" {
		where += " AND project_id = ?"
		args = append(args, f.ProjectID)
	}
	if f.Phase != "" {
		where += " AND phase = ?"
		args = append(args, f.Phase)
	}
	if cursor != "" {
		key, id, cerr := decodeJobCursor(cursor)
		if cerr != nil {
			return nil, "", cerr
		}
		// SQLite 无行值比较类型转换；用显式 OR 组合保证 (sortExpr,id) 的 keyset 语义。
		where += fmt.Sprintf(" AND (%s %s ? OR (%s = ? AND id %s ?))", sortExpr, cmp, sortExpr, cmp)
		args = append(args, key, key, id)
	}
	args = append(args, pageSize+1)
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+jobSelectColumns+jobPageExtraColumns(dialectSQLite)+` FROM jobs WHERE `+where+
			" ORDER BY "+sortExpr+" "+dir+", id "+dir+" LIMIT ?", args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := make([]JobPageRow, 0, pageSize)
	for rows.Next() {
		row, err := sqScanJobPageRow(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > pageSize {
		last := out[pageSize-1]
		next = encodeJobCursor(jobPageSortValue(f, last), last.Job.ID)
		out = out[:pageSize]
	}
	return out, next, nil
}

func (s *SQLiteStore) PhaseCounts(ctx context.Context, tenantID, projectID string) (map[string]int, error) {
	args := []any{tenantID}
	where := "tenant_id = ?"
	if projectID != "" {
		where += " AND project_id = ?"
		args = append(args, projectID)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT phase, count(*) FROM jobs WHERE `+where+` GROUP BY phase`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var phase string
		var n int
		if err := rows.Scan(&phase, &n); err != nil {
			return nil, err
		}
		out[phase] = n
	}
	return out, rows.Err()
}

func sqIsUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
