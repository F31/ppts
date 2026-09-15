package artifact

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/tenant"
)

type PGStore struct {
	pool *pgxpool.Pool
}

func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

func (s *PGStore) Create(ctx context.Context, tenantID string, in NewArtifact) (*Artifact, error) {
	var a *Artifact
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		a, e = scan(tx.QueryRow(ctx, `INSERT INTO artifacts
			(id, tenant_id, project_id, snapshot_hash, format, object_key, content_hash, size_bytes)
			VALUES (gen_random_uuid(), $1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (tenant_id, project_id, snapshot_hash, format) DO UPDATE
			  SET object_key=artifacts.object_key
			RETURNING id, tenant_id, project_id, snapshot_hash, format, object_key, content_hash, size_bytes, created_at`,
			tenantID, in.ProjectID, in.SnapshotHash, string(in.Format), in.ObjectKey, in.ContentHash, in.SizeBytes))
		return e
	})
	return a, err
}

func (s *PGStore) Get(ctx context.Context, tenantID, id string) (*Artifact, error) {
	var a *Artifact
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		a, e = scan(tx.QueryRow(ctx, `SELECT id, tenant_id, project_id, snapshot_hash, format,
			object_key, content_hash, size_bytes, created_at FROM artifacts WHERE id=$1 AND tenant_id=$2`, id, tenantID))
		return e
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

// ListByProject 返回项目下全部产物（按创建时间倒序），供成品与版本页按快照聚合展示（B3-M1）。
func (s *PGStore) ListByProject(ctx context.Context, tenantID, projectID string) ([]*Artifact, error) {
	items := []*Artifact{}
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, qErr := tx.Query(ctx, `SELECT id, tenant_id, project_id, snapshot_hash, format,
			object_key, content_hash, size_bytes, created_at FROM artifacts
			WHERE project_id=$1 AND tenant_id=$2 ORDER BY created_at DESC`, projectID, tenantID)
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		for rows.Next() {
			var a Artifact
			var format string
			if sErr := rows.Scan(&a.ID, &a.TenantID, &a.ProjectID, &a.SnapshotHash, &format,
				&a.ObjectKey, &a.ContentHash, &a.SizeBytes, &a.CreatedAt); sErr != nil {
				return sErr
			}
			a.Format = Format(format)
			items = append(items, &a)
		}
		return rows.Err()
	})
	return items, err
}

func scan(row pgx.Row) (*Artifact, error) {
	var a Artifact
	var format string
	if err := row.Scan(&a.ID, &a.TenantID, &a.ProjectID, &a.SnapshotHash, &format,
		&a.ObjectKey, &a.ContentHash, &a.SizeBytes, &a.CreatedAt); err != nil {
		return nil, err
	}
	a.Format = Format(format)
	return &a, nil
}
