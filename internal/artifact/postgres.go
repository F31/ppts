package artifact

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PGStore struct {
	pool *pgxpool.Pool
}

func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

func (s *PGStore) Create(ctx context.Context, tenantID string, in NewArtifact) (*Artifact, error) {
	row := s.pool.QueryRow(ctx, `INSERT INTO artifacts
		(id, tenant_id, project_id, snapshot_hash, format, object_key, content_hash, size_bytes)
		VALUES (gen_random_uuid(), $1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (tenant_id, project_id, snapshot_hash, format) DO UPDATE
		  SET object_key=artifacts.object_key
		RETURNING id, tenant_id, project_id, snapshot_hash, format, object_key, content_hash, size_bytes, created_at`,
		tenantID, in.ProjectID, in.SnapshotHash, string(in.Format), in.ObjectKey, in.ContentHash, in.SizeBytes)
	return scan(row)
}

func (s *PGStore) Get(ctx context.Context, tenantID, id string) (*Artifact, error) {
	a, err := scan(s.pool.QueryRow(ctx, `SELECT id, tenant_id, project_id, snapshot_hash, format,
		object_key, content_hash, size_bytes, created_at FROM artifacts WHERE id=$1 AND tenant_id=$2`, id, tenantID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
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
