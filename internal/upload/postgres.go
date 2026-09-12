package upload

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGUploadStore 以 PostgreSQL 实现 UploadStore。
type PGUploadStore struct {
	pool *pgxpool.Pool
}

// NewPGUploadStore 创建存储。
func NewPGUploadStore(pool *pgxpool.Pool) *PGUploadStore {
	return &PGUploadStore{pool: pool}
}

func (s *PGUploadStore) Create(ctx context.Context, in NewUpload) (*UploadSession, error) {
	return scanUpload(s.pool.QueryRow(ctx,
		`INSERT INTO uploads (id, tenant_id, project_id, filename, content_type, size_bytes,
		   delete_source_after, state, object_key)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,'pending',$8)
		 RETURNING id, tenant_id, project_id, filename, content_type, size_bytes,
		   delete_source_after, state, object_key, source_revision_id, job_id, created_at, updated_at`,
		in.ID, in.TenantID, in.ProjectID, in.Filename, in.ContentType, in.SizeBytes,
		in.DeleteSourceAfter, in.ObjectKey))
}

func (s *PGUploadStore) Get(ctx context.Context, tenantID, uploadID string) (*UploadSession, error) {
	return scanUpload(s.pool.QueryRow(ctx,
		`SELECT id, tenant_id, project_id, filename, content_type, size_bytes,
		   delete_source_after, state, object_key, source_revision_id, job_id, created_at, updated_at
		 FROM uploads WHERE id=$1 AND tenant_id=$2`, uploadID, tenantID))
}

func (s *PGUploadStore) Complete(ctx context.Context, tenantID, uploadID, sourceRevisionID, jobID string) (*UploadSession, error) {
	u, err := scanUpload(s.pool.QueryRow(ctx,
		`UPDATE uploads SET state='completed', source_revision_id=$3, job_id=$4, updated_at=now()
		 WHERE id=$1 AND tenant_id=$2 AND state='pending'
		 RETURNING id, tenant_id, project_id, filename, content_type, size_bytes,
		   delete_source_after, state, object_key, source_revision_id, job_id, created_at, updated_at`,
		uploadID, tenantID, sourceRevisionID, jobID))
	if errors.Is(err, ErrNotFound) {
		return s.classify(ctx, tenantID, uploadID, StateCompleted)
	}
	return u, err
}

func (s *PGUploadStore) Abort(ctx context.Context, tenantID, uploadID string) (*UploadSession, error) {
	u, err := scanUpload(s.pool.QueryRow(ctx,
		`UPDATE uploads SET state='aborted', updated_at=now()
		 WHERE id=$1 AND tenant_id=$2 AND state='pending'
		 RETURNING id, tenant_id, project_id, filename, content_type, size_bytes,
		   delete_source_after, state, object_key, source_revision_id, job_id, created_at, updated_at`,
		uploadID, tenantID))
	if errors.Is(err, ErrNotFound) {
		return s.classify(ctx, tenantID, uploadID, StateAborted)
	}
	return u, err
}

// classify 在条件更新未命中时读取现状，区分已完成/已中止/冲突。
// want 是本次尝试的目标状态：已完成会话对 Complete 幂等返回，
// 对 Abort 则返回 ErrAlreadyCompleted；已中止会话对 Abort 幂等返回，
// 对 Complete 返回 ErrAlreadyAborted。
func (s *PGUploadStore) classify(ctx context.Context, tenantID, uploadID string, want State) (*UploadSession, error) {
	u, err := s.Get(ctx, tenantID, uploadID)
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
