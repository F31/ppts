package narration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/google/uuid"

	"github.com/F31/ppts/internal/db"
)

// SQLiteStore 是 Store 的 SQLite 实现（单租户精简 profile）。
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore 创建 SQLite 讲稿存储。
func NewSQLiteStore(sqldb *sql.DB) *SQLiteStore { return &SQLiteStore{db: sqldb} }

var _ Store = (*SQLiteStore)(nil)

const sqScriptColumns = `id, tenant_id, project_id, slide_id, language, mode, status, revision, audio_revision, updated_at`

type rowScanner interface{ Scan(dest ...any) error }

func sqScanScript(row rowScanner) (*Revision, error) {
	var r Revision
	var updated string
	if err := row.Scan(&r.ID, &r.TenantID, &r.ProjectID, &r.SlideID, &r.Language,
		&r.Mode, &r.Status, &r.Revision, &r.AudioRevision, &updated); err != nil {
		return nil, err
	}
	r.UpdatedAt = db.ParseTime(updated)
	return &r, nil
}

// sqLoadScript 读取单份讲稿（不含分段）。
func (s *SQLiteStore) sqLoadScript(ctx context.Context, q queryer, tenantID, projectID, slideID, language string) (*Revision, error) {
	r, err := sqScanScript(q.QueryRowContext(ctx,
		`SELECT `+sqScriptColumns+` FROM narration_scripts
		 WHERE tenant_id = ? AND project_id = ? AND slide_id = ? AND language = ?`,
		tenantID, projectID, slideID, language))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

func (s *SQLiteStore) sqLoadSegments(ctx context.Context, q queryer, scriptID string) ([]*Segment, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT segment_id, display_text, spoken_text, source_refs, source_anchors, status
		 FROM narration_segments WHERE script_id = ? ORDER BY segment_id`, scriptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var segs []*Segment
	for rows.Next() {
		var seg Segment
		var refsRaw, anchorsRaw string
		if err := rows.Scan(&seg.SegmentID, &seg.DisplayText, &seg.SpokenText, &refsRaw, &anchorsRaw, &seg.Status); err != nil {
			return nil, err
		}
		if refsRaw != "" && refsRaw != "[]" {
			_ = json.Unmarshal([]byte(refsRaw), &seg.SourceRefs)
		}
		if anchorsRaw != "" && anchorsRaw != "[]" {
			_ = json.Unmarshal([]byte(anchorsRaw), &seg.SourceAnchors)
		}
		segs = append(segs, &seg)
	}
	return segs, rows.Err()
}

func (s *SQLiteStore) sqLoadAnchors(ctx context.Context, q queryer, scriptID string) (map[string][]SourceAnchor, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT segment_id, source_anchors FROM narration_segments WHERE script_id = ?`, scriptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]SourceAnchor{}
	for rows.Next() {
		var segmentID, data string
		if err := rows.Scan(&segmentID, &data); err != nil {
			return nil, err
		}
		var anchors []SourceAnchor
		if data != "" && data != "[]" {
			_ = json.Unmarshal([]byte(data), &anchors)
		}
		out[segmentID] = anchors
	}
	return out, rows.Err()
}

// sqWithSegments 填充分段后返回。
func (s *SQLiteStore) sqWithSegments(ctx context.Context, q queryer, r *Revision) (*Revision, error) {
	segs, err := s.sqLoadSegments(ctx, q, r.ID)
	if err != nil {
		return nil, err
	}
	r.Segments = segs
	return r, nil
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (s *SQLiteStore) Get(ctx context.Context, tenantID, projectID, slideID, language string) (*Revision, error) {
	r, err := s.sqLoadScript(ctx, s.db, tenantID, projectID, slideID, language)
	if err != nil {
		return nil, err
	}
	return s.sqWithSegments(ctx, s.db, r)
}

func (s *SQLiteStore) EnsureExists(ctx context.Context, tenantID, projectID, slideID, language string, mode ScriptMode) (*Revision, error) {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO narration_scripts (id, tenant_id, project_id, slide_id, language, mode, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(tenant_id, project_id, slide_id, language) DO NOTHING`,
		uuid.New().String(), tenantID, projectID, slideID, language, string(mode), db.Now(), db.Now()); err != nil {
		return nil, err
	}
	r, err := s.sqLoadScript(ctx, s.db, tenantID, projectID, slideID, language)
	if err != nil {
		return nil, err
	}
	return s.sqWithSegments(ctx, s.db, r)
}

func (s *SQLiteStore) Update(ctx context.Context, tenantID, projectID, slideID, language string, expected int64, segments []*Segment) (*Revision, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var scriptID string
	var status ScriptStatus
	var revision int64
	err = tx.QueryRowContext(ctx,
		`SELECT id, status, revision FROM narration_scripts
		 WHERE tenant_id = ? AND project_id = ? AND slide_id = ? AND language = ?`,
		tenantID, projectID, slideID, language).Scan(&scriptID, &status, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	// Editing creates a new draft, including for legacy locked scripts.
	// Keep the revision check atomic so concurrent edits cannot overwrite content.
	if revision != expected {
		latest, lerr := s.sqRevisionTx(ctx, tx, scriptID)
		if lerr != nil {
			return nil, lerr
		}
		return nil, &ErrConflict{Latest: latest}
	}
	existingAnchors, err := s.sqLoadAnchors(ctx, tx, scriptID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM narration_segments WHERE script_id = ?`, scriptID); err != nil {
		return nil, err
	}
	for _, seg := range segments {
		anchors := seg.SourceAnchors
		if anchors == nil {
			anchors = existingAnchors[seg.SegmentID]
		}
		if anchors == nil {
			anchors = []SourceAnchor{}
		}
		anchorBytes, err := json.Marshal(anchors)
		if err != nil {
			return nil, err
		}
		refsBytes, err := json.Marshal(nonNilStrings(seg.SourceRefs))
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO narration_segments (id, script_id, tenant_id, segment_id, display_text,
			   spoken_text, source_refs, source_anchors, status, revision, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'draft', 0, ?)`,
			uuid.New().String(), scriptID, tenantID, seg.SegmentID, seg.DisplayText,
			seg.SpokenText, string(refsBytes), string(anchorBytes), db.Now()); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE narration_scripts SET revision = revision + 1, status = 'draft', updated_at = ?
		 WHERE id = ?`, db.Now(), scriptID); err != nil {
		return nil, err
	}
	r, err := s.sqLoadScript(ctx, tx, tenantID, projectID, slideID, language)
	if err != nil {
		return nil, err
	}
	r, err = s.sqWithSegments(ctx, tx, r)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return r, nil
}

func (s *SQLiteStore) SetStatus(ctx context.Context, tenantID, projectID, slideID, language string, newStatus ScriptStatus) (*Revision, error) {
	var allowFrom ScriptStatus
	switch newStatus {
	case StatusApproved:
		allowFrom = StatusDraft
	case StatusLocked:
		allowFrom = StatusApproved
	default:
		return nil, errors.New("narration: unsupported status transition")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if newStatus == StatusApproved {
		allowFrom = StatusLocked
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE narration_scripts SET status = ?, updated_at = ?
		 WHERE tenant_id = ? AND project_id = ? AND slide_id = ? AND language = ? AND status IN (?, ?)`,
		string(newStatus), db.Now(), tenantID, projectID, slideID, language, string(allowFrom), string(StatusDraft))
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		cur, gerr := s.sqLoadScript(ctx, tx, tenantID, projectID, slideID, language)
		if gerr != nil {
			return nil, gerr
		}
		if cur.Status == StatusLocked && newStatus != StatusApproved {
			return nil, ErrLocked
		}
		return nil, errors.New("narration: status transition not allowed from " + string(cur.Status))
	}
	r, err := s.sqLoadScript(ctx, tx, tenantID, projectID, slideID, language)
	if err != nil {
		return nil, err
	}
	r, err = s.sqWithSegments(ctx, tx, r)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return r, nil
}

func (s *SQLiteStore) CountDraftSegments(ctx context.Context, tenantID, projectID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM narration_segments seg
		 JOIN narration_scripts n ON n.id = seg.script_id
		 WHERE n.tenant_id = ? AND n.project_id = ? AND seg.status = 'draft'`,
		tenantID, projectID).Scan(&n)
	return n, err
}

func (s *SQLiteStore) MarkAudioRevision(ctx context.Context, tenantID, projectID, slideID, language string, revision int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE narration_scripts SET audio_revision = ?, updated_at = ?
		 WHERE tenant_id = ? AND project_id = ? AND slide_id = ? AND language = ?`,
		revision, db.Now(), tenantID, projectID, slideID, language)
	return err
}

func (s *SQLiteStore) ListByProject(ctx context.Context, tenantID, projectID, language string) ([]*Revision, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+sqScriptColumns+` FROM narration_scripts
		 WHERE tenant_id = ? AND project_id = ? AND language = ? ORDER BY slide_id`,
		tenantID, projectID, language)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var revs []*Revision
	for rows.Next() {
		r, err := sqScanScript(rows)
		if err != nil {
			return nil, err
		}
		if _, err := s.sqWithSegments(ctx, s.db, r); err != nil {
			return nil, err
		}
		revs = append(revs, r)
	}
	return revs, rows.Err()
}

// sqRevisionTx 读取完整讲稿（含分段），供 ErrConflict.Latest。
func (s *SQLiteStore) sqRevisionTx(ctx context.Context, tx *sql.Tx, scriptID string) (*Revision, error) {
	r, err := sqScanScript(tx.QueryRowContext(ctx,
		`SELECT `+sqScriptColumns+` FROM narration_scripts WHERE id = ?`, scriptID))
	if err != nil {
		return nil, err
	}
	return s.sqWithSegments(ctx, tx, r)
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
