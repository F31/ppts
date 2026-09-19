package tenant

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/internal/integrations/objectstore"
)

// ErrNotSupported 表示单租户 SQLite profile 不支持该控制面能力（BYOS / 导出 / 远程存储切换）。
var ErrNotSupported = errors.New("tenant: not supported in sqlite single-tenant profile")

// SQLiteStore 是租户策略/状态/存储用量/对象清单的 SQLite 实现（单租户精简 profile）。
// 与 PGStore 同方法集，供 cmd 按驱动选择；本地租户由 db.EnsureLocalIdentity 播种。
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore 创建 SQLite 租户存储。
func NewSQLiteStore(sqldb *sql.DB) *SQLiteStore { return &SQLiteStore{db: sqldb} }

// ─── 策略 ────────────────────────────────────────────────────────────────────

func (s *SQLiteStore) GetPolicy(ctx context.Context, tenantID string) (*Policy, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT policy FROM tenants WHERE id = ?`, tenantID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTenantNotFound
	}
	if err != nil {
		return nil, err
	}
	p := &Policy{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), p); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func (s *SQLiteStore) SetPolicy(ctx context.Context, tenantID string, policy Policy) error {
	raw, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE tenants SET policy = ?, updated_at = ? WHERE id = ?`,
		string(raw), db.Now(), tenantID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTenantNotFound
	}
	return nil
}

func (s *SQLiteStore) ObjectStoreBackend(ctx context.Context, tenantID string) (string, error) {
	p, err := s.GetPolicy(ctx, tenantID)
	if err != nil {
		return "", err
	}
	return p.StorageBackend, nil
}

func (s *SQLiteStore) StorageRegion(ctx context.Context, tenantID string) (string, error) {
	p, err := s.GetPolicy(ctx, tenantID)
	if err != nil {
		return "", err
	}
	return p.StorageRegion, nil
}

func (s *SQLiteStore) ObjectEnvelopeEncryption(ctx context.Context, tenantID string) (bool, error) {
	p, err := s.GetPolicy(ctx, tenantID)
	if err != nil {
		return false, err
	}
	// 单租户本地 profile 不做信封加密（避免本地密钥管理复杂度）。
	return false && p.EnvelopeEncryption, nil
}

func (s *SQLiteStore) ListLifecyclePolicies(ctx context.Context) ([]LifecyclePolicySetting, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, policy FROM tenants WHERE status = 'active' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LifecyclePolicySetting
	for rows.Next() {
		var setting LifecyclePolicySetting
		var raw string
		if err := rows.Scan(&setting.TenantID, &raw); err != nil {
			return nil, err
		}
		if raw != "" {
			if err := json.Unmarshal([]byte(raw), &setting.Policy); err != nil {
				return nil, err
			}
		}
		out = append(out, setting)
	}
	return out, rows.Err()
}

// ─── 生命周期 ────────────────────────────────────────────────────────────────

func (s *SQLiteStore) Status(ctx context.Context, tenantID string) (Status, error) {
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT status FROM tenants WHERE id = ?`, tenantID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrTenantNotFound
	}
	if err != nil {
		return "", err
	}
	return Status(status), nil
}

func (s *SQLiteStore) TenantActive(ctx context.Context, tenantID string) (bool, error) {
	status, err := s.Status(ctx, tenantID)
	if err != nil {
		return false, err
	}
	return status == StatusActive, nil
}

func (s *SQLiteStore) Suspend(ctx context.Context, tenantID string) error {
	return s.setStatus(ctx, tenantID, StatusSuspended)
}

func (s *SQLiteStore) Resume(ctx context.Context, tenantID string) error {
	return s.setStatus(ctx, tenantID, StatusActive)
}

func (s *SQLiteStore) setStatus(ctx context.Context, tenantID string, status Status) error {
	res, err := s.db.ExecContext(ctx, `UPDATE tenants SET status = ?, updated_at = ? WHERE id = ?`,
		string(status), db.Now(), tenantID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTenantNotFound
	}
	return nil
}

// ─── 对象清单 ────────────────────────────────────────────────────────────────

func (s *SQLiteStore) RecordObject(ctx context.Context, key objectstore.ObjectKey, meta objectstore.ObjectMeta) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO object_inventory (object_key, tenant_id, project_id, revision, asset_type, asset_id, ext,
		   size_bytes, content_type, content_hash, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(object_key) DO UPDATE SET
		   size_bytes = excluded.size_bytes, content_type = excluded.content_type,
		   content_hash = excluded.content_hash, updated_at = excluded.updated_at`,
		key.String(), key.TenantID, key.ProjectID, key.Revision, key.AssetType, key.AssetID, key.Ext,
		meta.Size, meta.ContentType, meta.ContentHash, db.Now())
	return err
}

func (s *SQLiteStore) DeleteObjectRecord(ctx context.Context, key objectstore.ObjectKey) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM object_inventory WHERE object_key = ?`, key.String())
	return err
}

// ─── 存储用量 ────────────────────────────────────────────────────────────────

func (s *SQLiteStore) StorageUsage(ctx context.Context, tenantID string) (*StorageUsage, error) {
	if _, err := s.Status(ctx, tenantID); err != nil {
		return nil, err
	}
	out := &StorageUsage{TenantID: tenantID}
	err := s.db.QueryRowContext(ctx, `
		SELECT
		  COALESCE((SELECT SUM(size_bytes) FROM uploads WHERE tenant_id = ? AND state <> 'aborted'), 0),
		  COALESCE((SELECT COUNT(*) FROM uploads WHERE tenant_id = ? AND state <> 'aborted'), 0),
		  COALESCE((SELECT SUM(size_bytes) FROM artifacts WHERE tenant_id = ?), 0),
		  COALESCE((SELECT COUNT(*) FROM artifacts WHERE tenant_id = ?), 0),
		  COALESCE((SELECT SUM(size_bytes) FROM object_inventory oi
		    WHERE oi.tenant_id = ?
		      AND NOT EXISTS (SELECT 1 FROM uploads u WHERE u.tenant_id = ? AND u.state <> 'aborted' AND u.object_key = oi.object_key)
		      AND NOT EXISTS (SELECT 1 FROM artifacts a WHERE a.tenant_id = ? AND a.object_key = oi.object_key)), 0),
		  COALESCE((SELECT COUNT(*) FROM object_inventory oi
		    WHERE oi.tenant_id = ?
		      AND NOT EXISTS (SELECT 1 FROM uploads u WHERE u.tenant_id = ? AND u.state <> 'aborted' AND u.object_key = oi.object_key)
		      AND NOT EXISTS (SELECT 1 FROM artifacts a WHERE a.tenant_id = ? AND a.object_key = oi.object_key)), 0)`,
		tenantID, tenantID, tenantID, tenantID, tenantID, tenantID, tenantID, tenantID, tenantID, tenantID,
	).Scan(&out.SourceBytes, &out.SourceObjects, &out.ArtifactBytes, &out.ArtifactObjects,
		&out.OtherBytes, &out.OtherObjects)
	if err != nil {
		return nil, err
	}
	out.TotalBytes = out.SourceBytes + out.ArtifactBytes + out.OtherBytes
	return out, nil
}

// ─── 审计归档 ────────────────────────────────────────────────────────────────

func (s *SQLiteStore) ListAuditArchives(ctx context.Context, tenantID string, limit int) ([]ArchiveFile, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT object_key, size_bytes, updated_at
		FROM object_inventory
		WHERE tenant_id = ? AND asset_type = 'audit'
		ORDER BY updated_at DESC LIMIT ?`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArchiveFile
	for rows.Next() {
		var f ArchiveFile
		var updated string
		if err := rows.Scan(&f.ObjectKey, &f.SizeBytes, &updated); err != nil {
			return nil, err
		}
		f.UpdatedAt = db.ParseTime(updated)
		out = append(out, f)
	}
	return out, rows.Err()
}

// ─── 导出 / 擦除（单租户：擦除简化为删本地行；导出不支持）──────────────────────

// ExportTenant 在单租户 profile 下不支持（无多租户合规导出需求）。
func (s *SQLiteStore) ExportTenant(ctx context.Context, tenantID string, objects objectstore.ObjectStore) (*ExportManifest, error) {
	return nil, ErrNotSupported
}

// PurgeTenant 删除本地租户的全部业务数据行（不触碰对象存储，供本地重置）。
// 返回删除的总行数。
func (s *SQLiteStore) PurgeTenant(ctx context.Context, tenantID string, objects objectstore.ObjectStore) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	tables := []string{
		"job_events", "job_steps", "jobs",
		"narration_segments", "narration_scripts",
		"artifacts", "source_revisions", "uploads",
		"slide_script_sources", "project_tags", "project_share_links", "project_collaborators",
		"projects", "folders", "tags",
		"object_inventory", "usage_ledger", "quota_reservations", "tenant_quotas",
		"audit_events", "publications", "pronunciation_dictionaries", "model_gateways",
		"byos_credentials",
	}
	var total int64
	for _, t := range tables {
		res, err := tx.ExecContext(ctx, `DELETE FROM `+t+` WHERE tenant_id = ?`, tenantID)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return total, nil
}

// ─── BYOS（单租户不支持）──────────────────────────────────────────────────────

func (s *SQLiteStore) SetBYOSCredential(ctx context.Context, tenantID, credentialID, backend string, cfg BYOSConfig, kmsKeyID string, cipher CredentialCipher) error {
	return ErrNotSupported
}

func (s *SQLiteStore) GetBYOSCredential(ctx context.Context, tenantID, credentialID string, cipher CredentialCipher) (*BYOSCredential, error) {
	return nil, ErrNotSupported
}

func (s *SQLiteStore) RemoveBYOSCredential(ctx context.Context, tenantID, credentialID string) error {
	return ErrNotSupported
}

// 保证 time 被引用（ArchiveFile.UpdatedAt 使用）。
var _ = time.Time{}
