package upload

import (
	"context"
	"database/sql"
	"errors"

	"github.com/F31/ppts/internal/db"
)

// SQLiteStore 是 Store 的 SQLite 实现（单租户精简 profile）。
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore 创建 SQLite 上传会话存储。
func NewSQLiteStore(sqldb *sql.DB) *SQLiteStore { return &SQLiteStore{db: sqldb} }

var _ Store = (*SQLiteStore)(nil)

const sqUploadColumns = `id, tenant_id, project_id, filename, content_type, size_bytes,
	delete_source_after, state, object_key, source_revision_id, job_id, created_at, updated_at`

func sqScanUpload(row rowScanner) (*UploadSession, error) {
	var u UploadSession
	var deleteAfter int
	var created, updated string
	if err := row.Scan(&u.ID, &u.TenantID, &u.ProjectID, &u.Filename, &u.ContentType,
		&u.SizeBytes, &deleteAfter, &u.State, &u.ObjectKey,
		&u.SourceRevisionID, &u.JobID, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.DeleteSourceAfter = deleteAfter != 0
	u.CreatedAt = db.ParseTime(created)
	u.UpdatedAt = db.ParseTime(updated)
	return &u, nil
}

type rowScanner interface{ Scan(dest ...any) error }

func (s *SQLiteStore) Create(ctx context.Context, in NewUpload) (*UploadSession, error) {
	now := db.Now()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO uploads (id, tenant_id, project_id, filename, content_type, size_bytes,
		   delete_source_after, state, object_key, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?)`,
		in.ID, in.TenantID, in.ProjectID, in.Filename, in.ContentType, in.SizeBytes,
		boolToInt(in.DeleteSourceAfter), in.ObjectKey, now, now); err != nil {
		return nil, err
	}
	return sqScanUpload(s.db.QueryRowContext(ctx,
		`SELECT `+sqUploadColumns+` FROM uploads WHERE id = ? AND tenant_id = ?`, in.ID, in.TenantID))
}

func (s *SQLiteStore) Get(ctx context.Context, tenantID, uploadID string) (*UploadSession, error) {
	return sqScanUpload(s.db.QueryRowContext(ctx,
		`SELECT `+sqUploadColumns+` FROM uploads WHERE id = ? AND tenant_id = ?`, uploadID, tenantID))
}

func (s *SQLiteStore) Complete(ctx context.Context, tenantID, uploadID, sourceRevisionID, jobID string) (*UploadSession, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE uploads SET state = 'completed', source_revision_id = ?, job_id = ?, updated_at = ?
		 WHERE id = ? AND tenant_id = ? AND state = 'pending'`,
		sourceRevisionID, jobID, db.Now(), uploadID, tenantID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return s.Get(ctx, tenantID, uploadID)
	}
	return s.classify(ctx, tenantID, uploadID, StateCompleted)
}

func (s *SQLiteStore) Abort(ctx context.Context, tenantID, uploadID string) (*UploadSession, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE uploads SET state = 'aborted', updated_at = ?
		 WHERE id = ? AND tenant_id = ? AND state = 'pending'`,
		db.Now(), uploadID, tenantID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return s.Get(ctx, tenantID, uploadID)
	}
	return s.classify(ctx, tenantID, uploadID, StateAborted)
}

// classify 在条件更新未命中时读取现状，区分已完成/已中止/冲突（与 PG classifyTx 同语义）。
func (s *SQLiteStore) classify(ctx context.Context, tenantID, uploadID string, want State) (*UploadSession, error) {
	u, err := s.Get(ctx, tenantID, uploadID)
	if err != nil {
		return nil, err
	}
	switch u.State {
	case StateCompleted:
		if want == StateCompleted {
			return u, nil
		}
		return nil, ErrAlreadyCompleted
	case StateAborted:
		if want == StateAborted {
			return u, nil
		}
		return nil, ErrAlreadyAborted
	default:
		return nil, ErrStateConflict
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
