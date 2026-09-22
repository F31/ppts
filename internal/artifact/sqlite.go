package artifact

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"

	"github.com/F31/ppts/internal/db"
)

// SQLiteStore 是 Store 的 SQLite 实现（单租户精简 profile）。
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore 创建 SQLite 成品存储。
func NewSQLiteStore(sqldb *sql.DB) *SQLiteStore { return &SQLiteStore{db: sqldb} }

const sqArtifactColumns = `id, tenant_id, project_id, snapshot_hash, format, object_key,
	content_hash, size_bytes, duration_ms, created_at`

func sqScanArtifact(row rowScanner) (*Artifact, error) {
	var a Artifact
	var format, created string
	if err := row.Scan(&a.ID, &a.TenantID, &a.ProjectID, &a.SnapshotHash, &format,
		&a.ObjectKey, &a.ContentHash, &a.SizeBytes, &a.DurationMS, &created); err != nil {
		return nil, err
	}
	a.Format = Format(format)
	a.CreatedAt = db.ParseTime(created)
	return &a, nil
}

func (s *SQLiteStore) Create(ctx context.Context, tenantID string, in NewArtifact) (*Artifact, error) {
	id := uuid.New().String()
	now := db.Now()
	// 冲突时保留既有 object_key/content_hash/size_bytes，仅刷新 duration_ms（与 PG 语义一致）。
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO artifacts (id, tenant_id, project_id, snapshot_hash, format, object_key,
		   content_hash, size_bytes, duration_ms, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(tenant_id, project_id, snapshot_hash, format) DO UPDATE
		   SET duration_ms = excluded.duration_ms`,
		id, tenantID, in.ProjectID, in.SnapshotHash, string(in.Format), in.ObjectKey,
		in.ContentHash, in.SizeBytes, in.DurationMS, now); err != nil {
		return nil, err
	}
	return sqScanArtifact(s.db.QueryRowContext(ctx,
		`SELECT `+sqArtifactColumns+` FROM artifacts
		 WHERE tenant_id = ? AND project_id = ? AND snapshot_hash = ? AND format = ?`,
		tenantID, in.ProjectID, in.SnapshotHash, string(in.Format)))
}

func (s *SQLiteStore) Get(ctx context.Context, tenantID, id string) (*Artifact, error) {
	a, err := sqScanArtifact(s.db.QueryRowContext(ctx,
		`SELECT `+sqArtifactColumns+` FROM artifacts WHERE id = ? AND tenant_id = ?`, id, tenantID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

func (s *SQLiteStore) ListByProject(ctx context.Context, tenantID, projectID string) ([]*Artifact, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+sqArtifactColumns+` FROM artifacts
		 WHERE project_id = ? AND tenant_id = ? ORDER BY created_at DESC`, projectID, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []*Artifact{}
	for rows.Next() {
		a, err := sqScanArtifact(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, a)
	}
	return items, rows.Err()
}

func (s *SQLiteStore) ListAll(ctx context.Context, tenantID string) ([]*Artifact, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT a.id, a.tenant_id, a.project_id, a.snapshot_hash, a.format, a.object_key,
		        a.content_hash, a.size_bytes, a.duration_ms, a.created_at, COALESCE(p.title, '')
		 FROM artifacts a
		 LEFT JOIN projects p ON p.id = a.project_id AND p.tenant_id = a.tenant_id
		 WHERE a.tenant_id = ? ORDER BY a.created_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []*Artifact{}
	for rows.Next() {
		var a Artifact
		var format, created string
		if err := rows.Scan(&a.ID, &a.TenantID, &a.ProjectID, &a.SnapshotHash, &format,
			&a.ObjectKey, &a.ContentHash, &a.SizeBytes, &a.DurationMS, &created, &a.ProjectName); err != nil {
			return nil, err
		}
		a.Format = Format(format)
		a.CreatedAt = db.ParseTime(created)
		items = append(items, &a)
	}
	return items, rows.Err()
}

var _ Store = (*SQLiteStore)(nil)
