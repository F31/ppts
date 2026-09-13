package tenant

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/F31/ppts/internal/integrations/objectstore"
)

// RecordObject records object metadata for storage accounting.
func (s *PGStore) RecordObject(ctx context.Context, key objectstore.ObjectKey, meta objectstore.ObjectMeta) error {
	if err := key.Validate(); err != nil {
		return err
	}
	if meta.Size < 0 {
		meta.Size = 0
	}
	return Run(ctx, s.pool, key.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO object_inventory
			  (object_key, tenant_id, project_id, revision, asset_type, asset_id, ext, size_bytes, content_type, content_hash, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,now())
			ON CONFLICT (object_key) DO UPDATE SET
			  size_bytes=EXCLUDED.size_bytes,
			  content_type=EXCLUDED.content_type,
			  content_hash=EXCLUDED.content_hash,
			  updated_at=now()`,
			key.String(), key.TenantID, key.ProjectID, key.Revision, key.AssetType, key.AssetID, key.Ext,
			meta.Size, meta.ContentType, meta.ContentHash)
		return err
	})
}

// DeleteObjectRecord removes object metadata for storage accounting.
func (s *PGStore) DeleteObjectRecord(ctx context.Context, key objectstore.ObjectKey) error {
	if err := key.Validate(); err != nil {
		return err
	}
	return Run(ctx, s.pool, key.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM object_inventory WHERE tenant_id=$1 AND object_key=$2`, key.TenantID, key.String())
		return err
	})
}
