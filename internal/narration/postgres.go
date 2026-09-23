package narration

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/tenant"
)

// PGStore 以 PostgreSQL 实现 Store。
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore 创建存储。
func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

func (s *PGStore) Get(ctx context.Context, tenantID, projectID string, sourceRevisionNo int, slideID, language string) (*Revision, error) {
	var rev *Revision
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		r, err := loadScriptTx(ctx, tx, tenantID, projectID, sourceRevisionNo, slideID, language)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		segs, err := loadSegmentsTx(ctx, tx, r.ID)
		if err != nil {
			return err
		}
		r.Segments = segs
		rev = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rev, nil
}

func (s *PGStore) EnsureExists(ctx context.Context, tenantID, projectID string, sourceRevisionNo int, slideID, language string, mode ScriptMode) (*Revision, error) {
	var rev *Revision
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO narration_scripts (id, tenant_id, project_id, slide_id, language, mode, source_revision_no)
			 VALUES (gen_random_uuid(), $1,$2,$3,$4,$5,$6)
			 ON CONFLICT (tenant_id, project_id, source_revision_no, slide_id, language) DO NOTHING`,
			tenantID, projectID, slideID, language, string(mode), sourceRevisionNo); err != nil {
			return err
		}
		r, err := loadScriptExactTx(ctx, tx, tenantID, projectID, sourceRevisionNo, slideID, language)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		segs, err := loadSegmentsTx(ctx, tx, r.ID)
		if err != nil {
			return err
		}
		r.Segments = segs
		rev = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rev, nil
}

func (s *PGStore) Update(ctx context.Context, tenantID, projectID string, sourceRevisionNo int, slideID, language string, expected int64, segments []*Segment) (*Revision, error) {
	var rev *Revision
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		script, err := loadScriptExactTx(ctx, tx, tenantID, projectID, sourceRevisionNo, slideID, language)
		if errors.Is(err, pgx.ErrNoRows) && sourceRevisionNo != 0 {
			script, err = forkLegacyTx(ctx, tx, tenantID, projectID, sourceRevisionNo, slideID, language)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		scriptID := script.ID
		// Editing a legacy locked script creates a new draft under the same revision check.
		if script.Revision != expected {
			return &ErrConflict{Latest: loadRevisionTx(ctx, tx, scriptID)}
		}
		existingAnchors, err := loadAnchorsTx(ctx, tx, scriptID)
		if err != nil {
			return err
		}
		// 整页替换分段（分段携带稳定 ID；语音/字幕按稳定 ID 复用）。
		if _, err := tx.Exec(ctx, `DELETE FROM narration_segments WHERE script_id=$1`, scriptID); err != nil {
			return err
		}
		for _, seg := range segments {
			anchors := seg.SourceAnchors
			if anchors == nil {
				anchors = existingAnchors[seg.SegmentID]
			}
			if anchors == nil {
				anchors = []SourceAnchor{}
			}
			// source_refs 为 NOT NULL：调用方未填时落空数组而不是 NULL（与 anchors 同样兜底）。
			refs := seg.SourceRefs
			if refs == nil {
				refs = []string{}
			}
			anchorBytes, err := json.Marshal(anchors)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO narration_segments (id, script_id, tenant_id, segment_id, display_text, spoken_text, source_refs, source_anchors, status, revision)
				 VALUES (gen_random_uuid(), $1,$2,$3,$4,$5,$6,$7,'draft',0)`,
				scriptID, tenantID, seg.SegmentID, seg.DisplayText, seg.SpokenText, refs, string(anchorBytes)); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx,
			`UPDATE narration_scripts SET revision=revision+1, status='draft', updated_at=now() WHERE id=$1`, scriptID); err != nil {
			return err
		}
		r, err := loadScriptExactTx(ctx, tx, tenantID, projectID, sourceRevisionNo, slideID, language)
		if err != nil {
			return err
		}
		segs, err := loadSegmentsTx(ctx, tx, r.ID)
		if err != nil {
			return err
		}
		r.Segments = segs
		rev = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rev, nil
}

func (s *PGStore) SetStatus(ctx context.Context, tenantID, projectID string, sourceRevisionNo int, slideID, language string, newStatus ScriptStatus) (*Revision, error) {
	var allowFrom ScriptStatus
	switch newStatus {
	case StatusApproved:
		allowFrom = StatusDraft
	case StatusLocked:
		allowFrom = StatusApproved
	default:
		return nil, errors.New("narration: unsupported status transition")
	}
	var rev *Revision
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := loadScriptExactTx(ctx, tx, tenantID, projectID, sourceRevisionNo, slideID, language); errors.Is(err, pgx.ErrNoRows) && sourceRevisionNo != 0 {
			if _, ferr := forkLegacyTx(ctx, tx, tenantID, projectID, sourceRevisionNo, slideID, language); ferr != nil && !errors.Is(ferr, pgx.ErrNoRows) {
				return ferr
			}
		} else if err != nil {
			return err
		}
		if newStatus == StatusApproved {
			allowFrom = StatusLocked
		}
		tag, err := tx.Exec(ctx,
			`UPDATE narration_scripts SET status=$6, updated_at=now()
			 WHERE tenant_id=$1 AND project_id=$2 AND slide_id=$3 AND language=$4 AND source_revision_no=$5 AND status IN ($7, $8)`,
			tenantID, projectID, slideID, language, sourceRevisionNo, string(newStatus), allowFrom, StatusDraft)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			// 目标可能不存在或状态不允许。
			cur, gerr := loadScriptTx(ctx, tx, tenantID, projectID, sourceRevisionNo, slideID, language)
			if errors.Is(gerr, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if gerr != nil {
				return gerr
			}
			if cur.Status == StatusLocked && newStatus != StatusApproved {
				return ErrLocked
			}
			return errors.New("narration: status transition not allowed from " + string(cur.Status))
		}
		r, err := loadScriptExactTx(ctx, tx, tenantID, projectID, sourceRevisionNo, slideID, language)
		if err != nil {
			return err
		}
		segs, err := loadSegmentsTx(ctx, tx, r.ID)
		if err != nil {
			return err
		}
		r.Segments = segs
		rev = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rev, nil
}

// CountDraftSegments 返回项目内仍处于 draft 状态的讲稿分段总数（生成前置检查用）。
func (s *PGStore) CountDraftSegments(ctx context.Context, tenantID, projectID string) (int, error) {
	var n int
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM narration_segments seg
			 JOIN narration_scripts n ON n.id = seg.script_id
			 WHERE n.tenant_id=$1 AND n.project_id=$2 AND seg.status='draft'`,
			tenantID, projectID).Scan(&n)
	})
	return n, err
}

// MarkAudioRevision 回写某讲稿最近一次成功配音对应的脚本修订号（配音任务完成时调用）。
func (s *PGStore) MarkAudioRevision(ctx context.Context, tenantID, projectID string, sourceRevisionNo int, slideID, language string, revision int64) error {
	return tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE narration_scripts SET audio_revision=$6, updated_at=now()
			 WHERE tenant_id=$1 AND project_id=$2 AND slide_id=$3 AND language=$4 AND source_revision_no=$5`,
			tenantID, projectID, slideID, language, sourceRevisionNo, revision)
		return err
	})
}

// ListByProject 返回指定源版本下的讲稿。sourceRevisionNo>0 时优先该版本行，缺少该版本行的
// slide 回退到 legacy(0)，并按 slide 去重（版本行优先）。sourceRevisionNo==0 只返回 legacy。
func (s *PGStore) ListByProject(ctx context.Context, tenantID, projectID string, sourceRevisionNo int, language string) ([]*Revision, error) {
	var revs []*Revision
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, qerr := tx.Query(ctx,
			`SELECT id, tenant_id, project_id, slide_id, language, mode, status, revision, audio_revision, source_revision_no, updated_at
			 FROM narration_scripts WHERE tenant_id=$1 AND project_id=$2 AND language=$3 AND source_revision_no IN ($4, 0)
			 ORDER BY slide_id, source_revision_no DESC`,
			tenantID, projectID, language, sourceRevisionNo)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		seen := map[string]bool{}
		for rows.Next() {
			var r Revision
			if serr := rows.Scan(&r.ID, &r.TenantID, &r.ProjectID, &r.SlideID, &r.Language,
				&r.Mode, &r.Status, &r.Revision, &r.AudioRevision, &r.SourceRevisionNo, &r.UpdatedAt); serr != nil {
				return serr
			}
			if seen[r.SlideID] {
				continue
			}
			seen[r.SlideID] = true
			segs, serr := loadSegmentsTx(ctx, tx, r.ID)
			if serr != nil {
				return serr
			}
			r.Segments = segs
			revs = append(revs, &r)
		}
		return rows.Err()
	})
	return revs, err
}

// loadScriptTx 读取指定源版本的讲稿，sourceRevisionNo>0 时精确优先、缺失回退 legacy(0)。
func loadScriptTx(ctx context.Context, tx pgx.Tx, tenantID, projectID string, sourceRevisionNo int, slideID, language string) (*Revision, error) {
	row := tx.QueryRow(ctx,
		`SELECT id, tenant_id, project_id, slide_id, language, mode, status, revision, audio_revision, source_revision_no, updated_at
		 FROM narration_scripts
		 WHERE tenant_id=$1 AND project_id=$2 AND slide_id=$3 AND language=$4 AND source_revision_no IN ($5, 0)
		 ORDER BY source_revision_no DESC LIMIT 1`,
		tenantID, projectID, slideID, language, sourceRevisionNo)
	var r Revision
	err := row.Scan(&r.ID, &r.TenantID, &r.ProjectID, &r.SlideID, &r.Language,
		&r.Mode, &r.Status, &r.Revision, &r.AudioRevision, &r.SourceRevisionNo, &r.UpdatedAt)
	return &r, err
}

// loadScriptExactTx 只匹配精确源版本（不回落 legacy）。
func loadScriptExactTx(ctx context.Context, tx pgx.Tx, tenantID, projectID string, sourceRevisionNo int, slideID, language string) (*Revision, error) {
	row := tx.QueryRow(ctx,
		`SELECT id, tenant_id, project_id, slide_id, language, mode, status, revision, audio_revision, source_revision_no, updated_at
		 FROM narration_scripts
		 WHERE tenant_id=$1 AND project_id=$2 AND slide_id=$3 AND language=$4 AND source_revision_no=$5`,
		tenantID, projectID, slideID, language, sourceRevisionNo)
	var r Revision
	err := row.Scan(&r.ID, &r.TenantID, &r.ProjectID, &r.SlideID, &r.Language,
		&r.Mode, &r.Status, &r.Revision, &r.AudioRevision, &r.SourceRevisionNo, &r.UpdatedAt)
	return &r, err
}

// forkLegacyTx 在指定源版本无行、但存在 legacy(0) 行时，从 legacy 复制一份（含分段）到该版本。
func forkLegacyTx(ctx context.Context, tx pgx.Tx, tenantID, projectID string, sourceRevisionNo int, slideID, language string) (*Revision, error) {
	legacy, err := loadScriptExactTx(ctx, tx, tenantID, projectID, 0, slideID, language)
	if err != nil {
		return nil, err
	}
	var newID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO narration_scripts (id, tenant_id, project_id, slide_id, language, mode, status, revision, audio_revision, source_revision_no)
		 VALUES (gen_random_uuid(), $1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`,
		tenantID, projectID, slideID, language, string(legacy.Mode), string(legacy.Status),
		legacy.Revision, legacy.AudioRevision, sourceRevisionNo).Scan(&newID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO narration_segments (id, script_id, tenant_id, segment_id, display_text, spoken_text, source_refs, source_anchors, status, revision)
		 SELECT gen_random_uuid(), $1, tenant_id, segment_id, display_text, spoken_text, source_refs, source_anchors, status, revision
		   FROM narration_segments WHERE script_id=$2`, newID, legacy.ID); err != nil {
		return nil, err
	}
	return loadScriptExactTx(ctx, tx, tenantID, projectID, sourceRevisionNo, slideID, language)
}

func loadSegmentsTx(ctx context.Context, tx pgx.Tx, scriptID string) ([]*Segment, error) {
	rows, err := tx.Query(ctx,
		`SELECT segment_id, display_text, spoken_text, source_refs, source_anchors, status
		 FROM narration_segments WHERE script_id=$1 ORDER BY segment_id`, scriptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var segs []*Segment
	for rows.Next() {
		var seg Segment
		var refs []string
		var anchorBytes []byte
		if err := rows.Scan(&seg.SegmentID, &seg.DisplayText, &seg.SpokenText, &refs, &anchorBytes, &seg.Status); err != nil {
			return nil, err
		}
		seg.SourceRefs = refs
		if len(anchorBytes) > 0 {
			if err := json.Unmarshal(anchorBytes, &seg.SourceAnchors); err != nil {
				return nil, err
			}
		}
		segs = append(segs, &seg)
	}
	return segs, rows.Err()
}

func loadAnchorsTx(ctx context.Context, tx pgx.Tx, scriptID string) (map[string][]SourceAnchor, error) {
	rows, err := tx.Query(ctx, `SELECT segment_id, source_anchors FROM narration_segments WHERE script_id=$1`, scriptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]SourceAnchor{}
	for rows.Next() {
		var segmentID string
		var data []byte
		if err := rows.Scan(&segmentID, &data); err != nil {
			return nil, err
		}
		var anchors []SourceAnchor
		if len(data) > 0 {
			if err := json.Unmarshal(data, &anchors); err != nil {
				return nil, err
			}
		}
		out[segmentID] = anchors
	}
	return out, rows.Err()
}

// loadRevisionTx 读取最新完整讲稿（供 ErrConflict.Latest）。
func loadRevisionTx(ctx context.Context, tx pgx.Tx, scriptID string) *Revision {
	var r Revision
	row := tx.QueryRow(ctx,
		`SELECT id, tenant_id, project_id, slide_id, language, mode, status, revision, audio_revision, source_revision_no, updated_at
		 FROM narration_scripts WHERE id=$1`, scriptID)
	if err := row.Scan(&r.ID, &r.TenantID, &r.ProjectID, &r.SlideID, &r.Language,
		&r.Mode, &r.Status, &r.Revision, &r.AudioRevision, &r.SourceRevisionNo, &r.UpdatedAt); err != nil {
		return nil
	}
	if segs, err := loadSegmentsTx(ctx, tx, scriptID); err == nil {
		r.Segments = segs
	}
	return &r
}
