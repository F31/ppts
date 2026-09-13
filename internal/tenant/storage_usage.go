package tenant

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// StorageUsage summarizes DB-known object sizes for a tenant.
type StorageUsage struct {
	TenantID        string
	SourceBytes     int64
	ArtifactBytes   int64
	OtherBytes      int64
	TotalBytes      int64
	SourceObjects   int64
	ArtifactObjects int64
	OtherObjects    int64
}

// StorageUsage returns storage usage derived from metadata tables and object_inventory.
func (s *PGStore) StorageUsage(ctx context.Context, tenantID string) (*StorageUsage, error) {
	if _, err := s.Status(ctx, tenantID); err != nil {
		return nil, err
	}
	out := &StorageUsage{TenantID: tenantID}
	err := Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT
			  COALESCE((SELECT SUM(size_bytes) FROM uploads WHERE tenant_id=$1 AND state <> 'aborted'), 0),
			  COALESCE((SELECT COUNT(*) FROM uploads WHERE tenant_id=$1 AND state <> 'aborted'), 0),
			  COALESCE((SELECT SUM(size_bytes) FROM artifacts WHERE tenant_id=$1), 0),
			  COALESCE((SELECT COUNT(*) FROM artifacts WHERE tenant_id=$1), 0),
			  COALESCE((SELECT SUM(size_bytes) FROM object_inventory oi
			    WHERE oi.tenant_id=$1
			      AND NOT EXISTS (SELECT 1 FROM uploads u WHERE u.tenant_id=$1 AND u.state <> 'aborted' AND u.object_key=oi.object_key)
			      AND NOT EXISTS (SELECT 1 FROM artifacts a WHERE a.tenant_id=$1 AND a.object_key=oi.object_key)), 0),
			  COALESCE((SELECT COUNT(*) FROM object_inventory oi
			    WHERE oi.tenant_id=$1
			      AND NOT EXISTS (SELECT 1 FROM uploads u WHERE u.tenant_id=$1 AND u.state <> 'aborted' AND u.object_key=oi.object_key)
			      AND NOT EXISTS (SELECT 1 FROM artifacts a WHERE a.tenant_id=$1 AND a.object_key=oi.object_key)), 0)`, tenantID).
			Scan(&out.SourceBytes, &out.SourceObjects, &out.ArtifactBytes, &out.ArtifactObjects, &out.OtherBytes, &out.OtherObjects)
	})
	if err != nil {
		return nil, err
	}
	out.TotalBytes = out.SourceBytes + out.ArtifactBytes + out.OtherBytes
	return out, nil
}
