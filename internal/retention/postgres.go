package retention

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/tenant"
)

// PGStore 以 PostgreSQL 实现 Store。
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore 创建存储。
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

// ListTenants 读取控制面租户列表（tenants 不在 RLS 保护范围）。
func (s *PGStore) ListTenants(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, "SELECT id FROM tenants ORDER BY created_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *PGStore) PendingUploadsBefore(ctx context.Context, tenantID string, cutoff time.Time) ([]OrphanUpload, error) {
	var out []OrphanUpload
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id, object_key FROM uploads
			 WHERE tenant_id=$1 AND state='pending' AND created_at < $2`, tenantID, cutoff)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var o OrphanUpload
			if err := rows.Scan(&o.UploadID, &o.ObjectKey); err != nil {
				return err
			}
			out = append(out, o)
		}
		return rows.Err()
	})
	return out, err
}

func (s *PGStore) AbortUpload(ctx context.Context, tenantID, uploadID string) error {
	return tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE uploads SET state='aborted', updated_at=now()
			 WHERE tenant_id=$1 AND id=$2 AND state='pending'`, tenantID, uploadID)
		return err
	})
}

func (s *PGStore) SourcesToDelete(ctx context.Context, tenantID string, now time.Time) ([]SourceToDelete, error) {
	var out []SourceToDelete
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT sr.id::text, sr.object_key
			 FROM source_revisions sr
			 JOIN projects p ON p.id = sr.project_id AND p.tenant_id = sr.tenant_id
			 WHERE sr.tenant_id = $1
			   AND sr.source_deleted_at IS NULL
			   AND (
			     (p.source_retention_days IS NOT NULL
			        AND sr.created_at < $2::timestamptz - (p.source_retention_days || ' days')::interval)
			     OR (
			       p.delete_source_after = true
			       AND EXISTS (
			         SELECT 1 FROM uploads u
			         JOIN jobs j ON j.id::text = u.job_id AND j.tenant_id = u.tenant_id
			         WHERE u.tenant_id = sr.tenant_id
			           AND u.source_revision_id = sr.id::text
			           AND u.state = 'completed'
			           AND u.delete_source_after = true
			           AND j.state = 'succeeded'
			       )
			     )
			   )`, tenantID, now)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var src SourceToDelete
			if err := rows.Scan(&src.RevisionID, &src.ObjectKey); err != nil {
				return err
			}
			out = append(out, src)
		}
		return rows.Err()
	})
	return out, err
}

func (s *PGStore) MarkSourceDeleted(ctx context.Context, tenantID, revisionID string) error {
	return tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE source_revisions SET source_deleted_at=now()
			 WHERE tenant_id=$1 AND id=$2 AND source_deleted_at IS NULL`, tenantID, revisionID)
		return err
	})
}
