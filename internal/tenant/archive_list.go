package tenant

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// ArchiveFile is a persisted audit archive object descriptor.
type ArchiveFile struct {
	ObjectKey string
	SizeBytes int64
	UpdatedAt time.Time
}

// ListAuditArchives returns audit archive objects for a tenant in descending order.
// Archive objects are recorded in object_inventory by the worker archiver.
func (s *PGStore) ListAuditArchives(ctx context.Context, tenantID string, limit int) ([]ArchiveFile, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	var out []ArchiveFile
	err := Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT object_key, size_bytes, updated_at
			FROM object_inventory
			WHERE tenant_id=$1 AND asset_type='audit' AND revision='archive'
			ORDER BY updated_at DESC
			LIMIT $2`, tenantID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var f ArchiveFile
			if err := rows.Scan(&f.ObjectKey, &f.SizeBytes, &f.UpdatedAt); err != nil {
				return err
			}
			out = append(out, f)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []ArchiveFile{}
	}
	return out, nil
}
