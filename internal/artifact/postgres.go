package artifact

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/tenant"
)

// artifactColumns 是 Artifact 的**单一列清单**：Create 的 RETURNING、Get、ListByProject 与
// scan 全部复用同一顺序（同 pipeline.jobSelectColumns 的做法），避免新增列时只改一处导致
// 扫描错位——这类错位在编译期不可见，只会在运行时把值读进错误的字段。
const artifactColumns = `id, tenant_id, project_id, snapshot_hash, format, object_key,
	content_hash, size_bytes, duration_ms, created_at`

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
			(id, tenant_id, project_id, snapshot_hash, format, object_key, content_hash, size_bytes, duration_ms)
			VALUES (gen_random_uuid(), $1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (tenant_id, project_id, snapshot_hash, format) DO UPDATE
			  SET object_key=artifacts.object_key, duration_ms=EXCLUDED.duration_ms
			RETURNING `+artifactColumns,
			tenantID, in.ProjectID, in.SnapshotHash, string(in.Format), in.ObjectKey, in.ContentHash, in.SizeBytes, in.DurationMS))
		return e
	})
	return a, err
}

func (s *PGStore) Get(ctx context.Context, tenantID, id string) (*Artifact, error) {
	var a *Artifact
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		a, e = scan(tx.QueryRow(ctx, `SELECT `+artifactColumns+
			` FROM artifacts WHERE id=$1 AND tenant_id=$2`, id, tenantID))
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
		rows, qErr := tx.Query(ctx, `SELECT `+artifactColumns+` FROM artifacts
			WHERE project_id=$1 AND tenant_id=$2 ORDER BY created_at DESC`, projectID, tenantID)
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		for rows.Next() {
			a, sErr := scanRows(rows)
			if sErr != nil {
				return sErr
			}
			items = append(items, a)
		}
		return rows.Err()
	})
	return items, err
}

// ListAll 返回租户内全部项目成品（按创建时间倒序），供跨项目成品库（B5-M2）。
// 列顺序必须与 artifactColumns 完全一致，仅表别名 a. 前缀区别；project_title 附在其后，
// 由 scanRowsWithProject 按相同顺序扫描（同 jobSelectColumns/artifactColumns 的单一列清单原则，
// 避免顺序错位这类编译期不可见的运行期故障）。
func (s *PGStore) ListAll(ctx context.Context, tenantID string) ([]*Artifact, error) {
	items := []*Artifact{}
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, qErr := tx.Query(ctx, `SELECT `+artifactColumns+`,
			COALESCE(p.title, '') AS project_title
			FROM artifacts a
			LEFT JOIN projects p ON p.id = a.project_id AND p.tenant_id = a.tenant_id
			WHERE a.tenant_id=$1 ORDER BY a.created_at DESC`, tenantID)
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		for rows.Next() {
			a, sErr := scanRowsWithProject(rows)
			if sErr != nil {
				return sErr
			}
			items = append(items, a)
		}
		return rows.Err()
	})
	return items, err
}

func scanRowsWithProject(row rowScanner) (*Artifact, error) {
	var a Artifact
	var format string
	if err := row.Scan(&a.ID, &a.TenantID, &a.ProjectID, &a.SnapshotHash, &format,
		&a.ObjectKey, &a.ContentHash, &a.SizeBytes, &a.DurationMS, &a.CreatedAt, &a.ProjectName); err != nil {
		return nil, err
	}
	a.Format = Format(format)
	return &a, nil
}

// rowScanner 覆盖 pgx.Row 与 pgx.Rows 共有的 Scan，使单行与多行读取共用同一列顺序。
type rowScanner interface {
	Scan(dest ...any) error
}

func scanRows(row rowScanner) (*Artifact, error) {
	var a Artifact
	var format string
	if err := row.Scan(&a.ID, &a.TenantID, &a.ProjectID, &a.SnapshotHash, &format,
		&a.ObjectKey, &a.ContentHash, &a.SizeBytes, &a.DurationMS, &a.CreatedAt); err != nil {
		return nil, err
	}
	a.Format = Format(format)
	return &a, nil
}

func scan(row pgx.Row) (*Artifact, error) {
	return scanRows(row)
}
