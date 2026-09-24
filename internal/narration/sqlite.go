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

const sqScriptColumns = `id, tenant_id, project_id, slide_id, language, mode, status, revision, audio_revision, source_revision_no, updated_at`

type rowScanner interface{ Scan(dest ...any) error }

func sqScanScript(row rowScanner) (*Revision, error) {
	var r Revision
	var updated string
	if err := row.Scan(&r.ID, &r.TenantID, &r.ProjectID, &r.SlideID, &r.Language,
		&r.Mode, &r.Status, &r.Revision, &r.AudioRevision, &r.SourceRevisionNo, &updated); err != nil {
		return nil, err
	}
	r.UpdatedAt = db.ParseTime(updated)
	return &r, nil
}

// sqLoadScript 读取指定源版本的讲稿（不含分段）。sourceRevisionNo>0 时优先精确匹配，
// 缺失则回退到 legacy(source_revision_no=0)，保证存量稿在任意版本仍可见。
func (s *SQLiteStore) sqLoadScript(ctx context.Context, q queryer, tenantID, projectID string, sourceRevisionNo int, slideID, language string) (*Revision, error) {
	r, err := sqScanScript(q.QueryRowContext(ctx,
		`SELECT `+sqScriptColumns+` FROM narration_scripts
		 WHERE tenant_id = ? AND project_id = ? AND slide_id = ? AND language = ?
		   AND source_revision_no IN (?, 0)
		 ORDER BY source_revision_no DESC LIMIT 1`,
		tenantID, projectID, slideID, language, sourceRevisionNo))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// sqLoadScriptExact 只匹配精确源版本（不回落 legacy），用于 EnsureExists/分叉判断。
func (s *SQLiteStore) sqLoadScriptExact(ctx context.Context, q queryer, tenantID, projectID string, sourceRevisionNo int, slideID, language string) (*Revision, error) {
	r, err := sqScanScript(q.QueryRowContext(ctx,
		`SELECT `+sqScriptColumns+` FROM narration_scripts
		 WHERE tenant_id = ? AND project_id = ? AND slide_id = ? AND language = ? AND source_revision_no = ?`,
		tenantID, projectID, slideID, language, sourceRevisionNo))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// sqForkLegacy 在指定源版本无行、但存在 legacy(0) 行时，从 legacy 复制一份（含分段）到该版本。
// 返回复制后的新行；无 legacy 时返回 ErrNotFound。用于「在旧版本上首次编辑/审批」即分叉，
// 既保留 legacy 原稿，又让该版本此后独立。
func (s *SQLiteStore) sqForkLegacy(ctx context.Context, tx *sql.Tx, tenantID, projectID string, sourceRevisionNo int, slideID, language string) (*Revision, error) {
	legacy, err := s.sqLoadScriptExact(ctx, tx, tenantID, projectID, 0, slideID, language)
	if err != nil {
		return nil, err
	}
	newID := uuid.New().String()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO narration_scripts (id, tenant_id, project_id, slide_id, language, mode, status, revision, audio_revision, source_revision_no, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		newID, tenantID, projectID, slideID, language, string(legacy.Mode), string(legacy.Status),
		legacy.Revision, legacy.AudioRevision, sourceRevisionNo, db.Now(), db.Now()); err != nil {
		return nil, err
	}
	if err := s.sqForkSegments(ctx, tx, legacy.ID, newID, tenantID); err != nil {
		return nil, err
	}
	return s.sqLoadScriptExact(ctx, tx, tenantID, projectID, sourceRevisionNo, slideID, language)
}

// sqForkSegments 逐条复制 legacy 分段到新讲稿（保证每行生成独立 uuid）。
func (s *SQLiteStore) sqForkSegments(ctx context.Context, tx *sql.Tx, fromScriptID, toScriptID, tenantID string) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT segment_id, display_text, spoken_text, source_refs, source_anchors, status, revision
		   FROM narration_segments WHERE script_id = ?`, fromScriptID)
	if err != nil {
		return err
	}
	type segRow struct {
		segmentID, display, spoken, refs, anchors, status string
		revision                                          int64
	}
	var segs []segRow
	for rows.Next() {
		var r segRow
		if err := rows.Scan(&r.segmentID, &r.display, &r.spoken, &r.refs, &r.anchors, &r.status, &r.revision); err != nil {
			rows.Close()
			return err
		}
		segs = append(segs, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, r := range segs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO narration_segments (id, script_id, tenant_id, segment_id, display_text, spoken_text, source_refs, source_anchors, status, revision, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			uuid.New().String(), toScriptID, tenantID, r.segmentID, r.display, r.spoken, r.refs, r.anchors, r.status, r.revision, db.Now()); err != nil {
			return err
		}
	}
	return nil
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

func (s *SQLiteStore) Get(ctx context.Context, tenantID, projectID string, sourceRevisionNo int, slideID, language string) (*Revision, error) {
	r, err := s.sqLoadScript(ctx, s.db, tenantID, projectID, sourceRevisionNo, slideID, language)
	if err != nil {
		return nil, err
	}
	return s.sqWithSegments(ctx, s.db, r)
}

func (s *SQLiteStore) EnsureExists(ctx context.Context, tenantID, projectID string, sourceRevisionNo int, slideID, language string, mode ScriptMode) (*Revision, error) {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO narration_scripts (id, tenant_id, project_id, slide_id, language, mode, source_revision_no, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(tenant_id, project_id, source_revision_no, slide_id, language) DO NOTHING`,
		uuid.New().String(), tenantID, projectID, slideID, language, string(mode), sourceRevisionNo, db.Now(), db.Now()); err != nil {
		return nil, err
	}
	r, err := s.sqLoadScriptExact(ctx, s.db, tenantID, projectID, sourceRevisionNo, slideID, language)
	if err != nil {
		return nil, err
	}
	return s.sqWithSegments(ctx, s.db, r)
}

func (s *SQLiteStore) Update(ctx context.Context, tenantID, projectID string, sourceRevisionNo int, slideID, language string, expected int64, segments []*Segment) (*Revision, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	script, err := s.sqLoadScriptExact(ctx, tx, tenantID, projectID, sourceRevisionNo, slideID, language)
	if errors.Is(err, ErrNotFound) && sourceRevisionNo != 0 {
		// 该版本首次编辑：从 legacy 分叉一份（保留 legacy 原稿）。
		script, err = s.sqForkLegacy(ctx, tx, tenantID, projectID, sourceRevisionNo, slideID, language)
	}
	if err != nil {
		return nil, err
	}
	scriptID := script.ID
	revision := script.Revision
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
	r, err := s.sqLoadScriptExact(ctx, tx, tenantID, projectID, sourceRevisionNo, slideID, language)
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

func (s *SQLiteStore) SetStatus(ctx context.Context, tenantID, projectID string, sourceRevisionNo int, slideID, language string, newStatus ScriptStatus) (*Revision, error) {
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

	if _, err := s.sqLoadScriptExact(ctx, tx, tenantID, projectID, sourceRevisionNo, slideID, language); errors.Is(err, ErrNotFound) && sourceRevisionNo != 0 {
		if _, ferr := s.sqForkLegacy(ctx, tx, tenantID, projectID, sourceRevisionNo, slideID, language); ferr != nil {
			return nil, ferr
		}
	} else if err != nil {
		return nil, err
	}

	if newStatus == StatusApproved {
		allowFrom = StatusLocked
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE narration_scripts SET status = ?, updated_at = ?
		 WHERE tenant_id = ? AND project_id = ? AND slide_id = ? AND language = ? AND source_revision_no = ? AND status IN (?, ?)`,
		string(newStatus), db.Now(), tenantID, projectID, slideID, language, sourceRevisionNo, string(allowFrom), string(StatusDraft))
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		cur, gerr := s.sqLoadScript(ctx, tx, tenantID, projectID, sourceRevisionNo, slideID, language)
		if gerr != nil {
			return nil, gerr
		}
		if cur.Status == StatusLocked && newStatus != StatusApproved {
			return nil, ErrLocked
		}
		return nil, errors.New("narration: status transition not allowed from " + string(cur.Status))
	}
	r, err := s.sqLoadScriptExact(ctx, tx, tenantID, projectID, sourceRevisionNo, slideID, language)
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

func (s *SQLiteStore) MarkAudioRevision(ctx context.Context, tenantID, projectID string, sourceRevisionNo int, slideID, language string, revision int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE narration_scripts SET audio_revision = ?, updated_at = ?
		 WHERE tenant_id = ? AND project_id = ? AND slide_id = ? AND language = ? AND source_revision_no = ?`,
		revision, db.Now(), tenantID, projectID, slideID, language, sourceRevisionNo)
	return err
}

// ListByProject 返回指定源版本下的讲稿。sourceRevisionNo>0 时优先该版本行，缺少该版本行的
// slide 回退到 legacy(0)，并按 slide 去重（版本行优先）。sourceRevisionNo==0 只返回 legacy。
func (s *SQLiteStore) ListByProject(ctx context.Context, tenantID, projectID string, sourceRevisionNo int, language string) ([]*Revision, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+sqScriptColumns+` FROM narration_scripts
		 WHERE tenant_id = ? AND project_id = ? AND language = ? AND source_revision_no IN (?, 0)
		 ORDER BY slide_id, source_revision_no DESC`,
		tenantID, projectID, language, sourceRevisionNo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var revs []*Revision
	seen := map[string]bool{}
	var collected []*Revision
	for rows.Next() {
		r, err := sqScanScript(rows)
		if err != nil {
			return nil, err
		}
		if seen[r.SlideID] {
			continue // 同一 slide 已取到更精确的版本行
		}
		seen[r.SlideID] = true
		collected = append(collected, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 先关闭游标再加载分段，避免在结果集未关闭时发起新查询（部分驱动会报错）。
	rows.Close()
	for _, r := range collected {
		if _, err := s.sqWithSegments(ctx, s.db, r); err != nil {
			return nil, err
		}
		revs = append(revs, r)
	}
	return revs, nil
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
