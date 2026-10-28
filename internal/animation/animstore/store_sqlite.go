package animstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/db"
)

// SQLiteStore 是 Store 的 SQLite 实现（单租户精简 profile）。
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore 创建 SQLite 动画规格存储。
func NewSQLiteStore(sqldb *sql.DB) *SQLiteStore { return &SQLiteStore{db: sqldb} }

var _ Store = (*SQLiteStore)(nil)

const sqAnimationColumns = `id, tenant_id, project_id, slide_id, language, source_revision_no,
	revision, status, preset_id, intensity, tracks, rendered_revision, updated_at`

type rowScanner interface{ Scan(dest ...any) error }

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func sqScanRevision(row rowScanner) (*animation.Revision, error) {
	var (
		r       animation.Revision
		status  string
		tracks  []byte
		updated string
	)
	if err := row.Scan(&r.ID, &r.TenantID, &r.ProjectID, &r.SlideID, &r.Language,
		&r.SourceRevisionNo, &r.Revision, &status, &r.PresetID, &r.Intensity,
		&tracks, &r.RenderedRevision, &updated); err != nil {
		return nil, err
	}
	parsed, err := unmarshalTracks(tracks)
	if err != nil {
		return nil, err
	}
	r.Status = animation.Status(status)
	r.Tracks = parsed
	r.UpdatedAt = db.ParseTime(updated)
	return &r, nil
}

// sqLoad 按源版本读取：sourceRevisionNo>0 时精确匹配优先，缺失回退 legacy(0)。
func (s *SQLiteStore) sqLoad(ctx context.Context, q queryer, key animation.Key) (*animation.Revision, error) {
	row := q.QueryRowContext(ctx,
		`SELECT `+sqAnimationColumns+` FROM animation_revisions
		 WHERE tenant_id = ? AND project_id = ? AND slide_id = ? AND language = ?
		   AND source_revision_no IN (?, 0)
		 ORDER BY source_revision_no DESC LIMIT 1`,
		key.TenantID, key.ProjectID, key.SlideID, key.Language, key.SourceRevisionNo)
	rev, err := sqScanRevision(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, animation.ErrNotFound
	}
	return rev, err
}

// sqLoadExact 只匹配精确源版本（不回落 legacy），用于写入与状态迁移。
func (s *SQLiteStore) sqLoadExact(ctx context.Context, q queryer, key animation.Key) (*animation.Revision, error) {
	row := q.QueryRowContext(ctx,
		`SELECT `+sqAnimationColumns+` FROM animation_revisions
		 WHERE tenant_id = ? AND project_id = ? AND slide_id = ? AND language = ? AND source_revision_no = ?`,
		key.TenantID, key.ProjectID, key.SlideID, key.Language, key.SourceRevisionNo)
	rev, err := sqScanRevision(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, animation.ErrNotFound
	}
	return rev, err
}

// Get 实现 Store。
func (s *SQLiteStore) Get(ctx context.Context, key animation.Key) (*animation.Revision, error) {
	key = key.Normalize()
	if err := key.Validate(); err != nil {
		return nil, err
	}
	return s.sqLoad(ctx, s.db, key)
}

// Put 实现 Store。写入与版本判定在同一事务内，避免"读完再写"被并发插队。
func (s *SQLiteStore) Put(ctx context.Context, rev *animation.Revision, expected int64) (*animation.Revision, error) {
	if rev == nil {
		return nil, fmt.Errorf("%w: nil revision", animation.ErrInvalidKey)
	}
	rev.Normalize()
	key := rev.Key()
	if err := key.Validate(); err != nil {
		return nil, err
	}
	tracks, err := marshalTracks(rev.Tracks)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	cur, err := s.sqLoadExact(ctx, tx, key)
	switch {
	case errors.Is(err, animation.ErrNotFound):
		if expected != 0 {
			// 记录不存在但调用方认为它存在：多半是别人删了，或前端拿着过期上下文。
			return nil, &animation.ErrConflict{}
		}
		id := uuid.New().String()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO animation_revisions (id, tenant_id, project_id, slide_id, language,
				source_revision_no, revision, status, preset_id, intensity, tracks, rendered_revision, created_at, updated_at)
			 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, key.TenantID, key.ProjectID, key.SlideID, key.Language, key.SourceRevisionNo,
			1, string(animation.StatusDraft), rev.PresetID, rev.Intensity, string(tracks), 0, db.Now(), db.Now()); err != nil {
			return nil, err
		}
		saved, err := s.sqLoadExact(ctx, tx, key)
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return saved, nil
	case err != nil:
		return nil, err
	}

	if cur.Status == animation.StatusLocked {
		return nil, animation.ErrLocked
	}
	if cur.Revision != expected {
		return nil, &animation.ErrConflict{Latest: cur}
	}

	res, err := tx.ExecContext(ctx,
		`UPDATE animation_revisions
		 SET revision = revision + 1, preset_id = ?, intensity = ?, tracks = ?, updated_at = ?
		 WHERE id = ? AND revision = ?`,
		rev.PresetID, rev.Intensity, string(tracks), db.Now(), cur.ID, expected)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		latest, lerr := s.sqLoadExact(ctx, tx, key)
		if lerr != nil {
			return nil, lerr
		}
		return nil, &animation.ErrConflict{Latest: latest}
	}

	saved, err := s.sqLoadExact(ctx, tx, key)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return saved, nil
}

// SetStatus 实现 Store。用条件 UPDATE 表达状态机，避免"先读后判再写"的竞态。
func (s *SQLiteStore) SetStatus(ctx context.Context, key animation.Key, target animation.Status) (*animation.Revision, error) {
	key = key.Normalize()
	if err := key.Validate(); err != nil {
		return nil, err
	}
	var allowedFrom animation.Status
	switch target {
	case animation.StatusApproved:
		allowedFrom = animation.StatusLocked // 与 draft 一起作为允许的起点
	case animation.StatusLocked:
		allowedFrom = animation.StatusApproved
	default:
		return nil, fmt.Errorf("%w: unknown target %q", animation.ErrStatusTransition, target)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	cur, err := s.sqLoadExact(ctx, tx, key)
	if errors.Is(err, animation.ErrNotFound) {
		return nil, animation.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !animation.AllowedTransition(cur.Status, target) {
		if cur.Status == animation.StatusLocked {
			return nil, animation.ErrLocked
		}
		return nil, fmt.Errorf("%w: %s → %s", animation.ErrStatusTransition, cur.Status, target)
	}

	res, err := tx.ExecContext(ctx,
		`UPDATE animation_revisions SET status = ?, updated_at = ?
		 WHERE id = ? AND status IN (?, ?)`,
		string(target), db.Now(), cur.ID, string(allowedFrom), string(animation.StatusDraft))
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		latest, lerr := s.sqLoadExact(ctx, tx, key)
		if lerr != nil {
			return nil, lerr
		}
		return nil, &animation.ErrConflict{Latest: latest}
	}

	saved, err := s.sqLoadExact(ctx, tx, key)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return saved, nil
}
