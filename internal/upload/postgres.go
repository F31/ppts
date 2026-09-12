package upload

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/tenant"
)

// PGUploadStore 以 PostgreSQL 实现 UploadStore。
type PGUploadStore struct {
	pool *pgxpool.Pool
}

// NewPGUploadStore 创建存储。
func NewPGUploadStore(pool *pgxpool.Pool) *PGUploadStore {
	return &PGUploadStore{pool: pool}
}

const uploadColumns = `id, tenant_id, project_id, filename, content_type, size_bytes,
	delete_source_after, state, object_key, source_revision_id, job_id, created_at, updated_at`

func (s *PGUploadStore) Create(ctx context.Context, in NewUpload) (*UploadSession, error) {
	var u *UploadSession
	err := tenant.Run(ctx, s.pool, in.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		u, e = scanUpload(tx.QueryRow(ctx,
			`INSERT INTO uploads (id, tenant_id, project_id, filename, content_type, size_bytes,
			   delete_source_after, state, object_key)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,'pending',$8)
			 RETURNING `+uploadColumns,
			in.ID, in.TenantID, in.ProjectID, in.Filename, in.ContentType, in.SizeBytes,
			in.DeleteSourceAfter, in.ObjectKey))
		return e
	})
	return u, err
}

func (s *PGUploadStore) Get(ctx context.Context, tenantID, uploadID string) (*UploadSession, error) {
	var u *UploadSession
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		u, e = scanUpload(tx.QueryRow(ctx,
			`SELECT `+uploadColumns+` FROM uploads WHERE id=$1 AND tenant_id=$2`, uploadID, tenantID))
		return e
	})
	return u, err
}

func (s *PGUploadStore) Complete(ctx context.Context, tenantID, uploadID, sourceRevisionID, jobID string) (*UploadSession, error) {
	var u *UploadSession
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		updated, err := scanUpload(tx.QueryRow(ctx,
			`UPDATE uploads SET state='completed', source_revision_id=$3, job_id=$4, updated_at=now()
			 WHERE id=$1 AND tenant_id=$2 AND state='pending'
			 RETURNING `+uploadColumns,
			uploadID, tenantID, sourceRevisionID, jobID))
		if err == nil {
			u = updated
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		u, err = classifyTx(ctx, tx, tenantID, uploadID, StateCompleted)
		return err
	})
	return u, err
}

func (s *PGUploadStore) Abort(ctx context.Context, tenantID, uploadID string) (*UploadSession, error) {
	var u *UploadSession
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		updated, err := scanUpload(tx.QueryRow(ctx,
			`UPDATE uploads SET state='aborted', updated_at=now()
			 WHERE id=$1 AND tenant_id=$2 AND state='pending'
			 RETURNING `+uploadColumns,
			uploadID, tenantID))
		if err == nil {
			u = updated
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		u, err = classifyTx(ctx, tx, tenantID, uploadID, StateAborted)
		return err
	})
	return u, err
}

// classifyTx 在条件更新未命中时读取现状，区分已完成/已中止/冲突。
// want 是本次尝试的目标状态：已完成会话对 Complete 幂等返回，
// 对 Abort 则返回 ErrAlreadyCompleted；已中止会话对 Abort 幂等返回，
// 对 Complete 返回 ErrAlreadyAborted。
func classifyTx(ctx context.Context, tx pgx.Tx, tenantID, uploadID string, want State) (*UploadSession, error) {
	u, err := scanUpload(tx.QueryRow(ctx,
		`SELECT `+uploadColumns+` FROM uploads WHERE id=$1 AND tenant_id=$2`, uploadID, tenantID))
	if err != nil {
		return nil, err
	}
	switch u.State {
	case StateCompleted:
		if want == StateCompleted {
			return u, nil
		}
		return nil, ErrAlreadyCompleted
	case StateAborted:
		if want == StateAborted {
			return u, nil
		}
		return nil, ErrAlreadyAborted
	default:
		return nil, ErrStateConflict
	}
}

func scanUpload(row pgx.Row) (*UploadSession, error) {
	var u UploadSession
	err := row.Scan(&u.ID, &u.TenantID, &u.ProjectID, &u.Filename, &u.ContentType,
		&u.SizeBytes, &u.DeleteSourceAfter, &u.State, &u.ObjectKey,
		&u.SourceRevisionID, &u.JobID, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}
