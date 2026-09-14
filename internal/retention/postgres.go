package retention

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/integrations/objectstore"
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
			 JOIN tenants t ON t.id = sr.tenant_id
			 WHERE sr.tenant_id = $1
			   AND sr.source_deleted_at IS NULL
			   AND (
			     (COALESCE(p.source_retention_days, (t.policy->>'source_retention_days')::int) IS NOT NULL
			        AND sr.created_at < $2::timestamptz
			            - (COALESCE(p.source_retention_days, (t.policy->>'source_retention_days')::int) || ' days')::interval)
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

// DerivedToDelete 返回超过租户派生产物保留分档的对象清单。
// 分档字段：artifact_retention_days（导出成品）/audio_retention_days（配音音频）/render_retention_days（页面渲染图）。
func (s *PGStore) DerivedToDelete(ctx context.Context, tenantID string, now time.Time) ([]DerivedToDelete, error) {
	var out []DerivedToDelete
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT oi.object_key, oi.asset_type, oi.asset_id
			 FROM object_inventory oi
			 JOIN tenants t ON t.id = oi.tenant_id
			 WHERE oi.tenant_id = $1
			   AND (
			     (oi.asset_type = 'artifact' AND (t.policy->>'artifact_retention_days')::int > 0
			        AND oi.updated_at < $2::timestamptz - ((t.policy->>'artifact_retention_days')::int || ' days')::interval)
			     OR (oi.asset_type = 'audio' AND (t.policy->>'audio_retention_days')::int > 0
			        AND oi.updated_at < $2::timestamptz - ((t.policy->>'audio_retention_days')::int || ' days')::interval)
			     OR (oi.asset_type = 'render' AND (t.policy->>'render_retention_days')::int > 0
			        AND oi.updated_at < $2::timestamptz - ((t.policy->>'render_retention_days')::int || ' days')::interval)
			   )
			 ORDER BY oi.updated_at`, tenantID, now)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d DerivedToDelete
			if err := rows.Scan(&d.ObjectKey, &d.AssetType, &d.AssetID); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// DeleteDerivedRecord 删除派生产物清单记录；artifact 同时按 object_key 解析项目并删除 artifacts 表行。
func (s *PGStore) DeleteDerivedRecord(ctx context.Context, tenantID, objectKey, assetType string) error {
	return tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if assetType == "artifact" {
			if key, err := objectstore.Parse(objectKey); err == nil {
				if _, err := tx.Exec(ctx,
					`DELETE FROM artifacts
					 WHERE tenant_id=$1 AND project_id=$2 AND content_hash=$3 AND format=$4`,
					tenantID, key.ProjectID, key.AssetID, key.Ext); err != nil {
					return err
				}
			}
		}
		_, err := tx.Exec(ctx,
			`DELETE FROM object_inventory WHERE tenant_id=$1 AND object_key=$2`, tenantID, objectKey)
		return err
	})
}

func (s *PGStore) MarkSourceDeleted(ctx context.Context, tenantID, revisionID string) error {
	return tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE source_revisions SET source_deleted_at=now()
			 WHERE tenant_id=$1 AND id=$2 AND source_deleted_at IS NULL`, tenantID, revisionID)
		return err
	})
}

func (s *PGStore) StaleReservationsBefore(ctx context.Context, tenantID string, cutoff time.Time) ([]StaleReservation, error) {
	var out []StaleReservation
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id::text, logical_operation_id, usage_kind
			 FROM quota_reservations
			 WHERE tenant_id=$1 AND state='reserved' AND created_at < $2
			 ORDER BY created_at`, tenantID, cutoff)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r StaleReservation
			if err := rows.Scan(&r.ReservationID, &r.LogicalOperationID, &r.UsageKind); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

func (s *PGStore) ReleaseReservationByID(ctx context.Context, tenantID, reservationID string) error {
	return tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var kind, state string
		var reserved float64
		err := tx.QueryRow(ctx,
			`SELECT usage_kind, reserved_units, state
			 FROM quota_reservations
			 WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, reservationID).Scan(&kind, &reserved, &state)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		if state != "reserved" {
			return nil
		}
		if _, err := tx.Exec(ctx,
			`UPDATE tenant_quotas SET reserved_units=GREATEST(reserved_units-$3, 0), updated_at=now()
			 WHERE tenant_id=$1 AND usage_kind=$2`, tenantID, kind, reserved); err != nil {
			return err
		}
		_, err = tx.Exec(ctx,
			`UPDATE quota_reservations SET state='released', updated_at=now()
			 WHERE tenant_id=$1 AND id=$2`, tenantID, reservationID)
		return err
	})
}
