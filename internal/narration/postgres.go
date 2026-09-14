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

func (s *PGStore) Get(ctx context.Context, tenantID, projectID, slideID, language string) (*Revision, error) {
	var rev *Revision
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		r, err := loadScriptTx(ctx, tx, tenantID, projectID, slideID, language)
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

func (s *PGStore) EnsureExists(ctx context.Context, tenantID, projectID, slideID, language string, mode ScriptMode) (*Revision, error) {
	var rev *Revision
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO narration_scripts (id, tenant_id, project_id, slide_id, language, mode)
			 VALUES (gen_random_uuid(), $1,$2,$3,$4,$5)
			 ON CONFLICT (tenant_id, project_id, slide_id, language) DO NOTHING`,
			tenantID, projectID, slideID, language, string(mode)); err != nil {
			return err
		}
		r, err := loadScriptTx(ctx, tx, tenantID, projectID, slideID, language)
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

func (s *PGStore) Update(ctx context.Context, tenantID, projectID, slideID, language string, expected int64, segments []*Segment) (*Revision, error) {
	var rev *Revision
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var scriptID string
		var status ScriptStatus
		var revision int64
		err := tx.QueryRow(ctx,
			`SELECT id, status, revision FROM narration_scripts
			 WHERE tenant_id=$1 AND project_id=$2 AND slide_id=$3 AND language=$4
			 FOR UPDATE`, tenantID, projectID, slideID, language).
			Scan(&scriptID, &status, &revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if status == StatusLocked {
			return ErrLocked
		}
		if revision != expected {
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
			anchorBytes, err := json.Marshal(anchors)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO narration_segments (id, script_id, tenant_id, segment_id, display_text, spoken_text, source_refs, source_anchors, status, revision)
				 VALUES (gen_random_uuid(), $1,$2,$3,$4,$5,$6,$7,'draft',0)`,
				scriptID, tenantID, seg.SegmentID, seg.DisplayText, seg.SpokenText, seg.SourceRefs, string(anchorBytes)); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx,
			`UPDATE narration_scripts SET revision=revision+1, status='draft', updated_at=now() WHERE id=$1`, scriptID); err != nil {
			return err
		}
		r, err := loadScriptTx(ctx, tx, tenantID, projectID, slideID, language)
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

func (s *PGStore) SetStatus(ctx context.Context, tenantID, projectID, slideID, language string, newStatus ScriptStatus) (*Revision, error) {
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
		tag, err := tx.Exec(ctx,
			`UPDATE narration_scripts SET status=$5, updated_at=now()
			 WHERE tenant_id=$1 AND project_id=$2 AND slide_id=$3 AND language=$4 AND status=$6`,
			tenantID, projectID, slideID, language, string(newStatus), allowFrom)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			// 目标可能不存在或状态不允许。
			cur, gerr := loadScriptTx(ctx, tx, tenantID, projectID, slideID, language)
			if errors.Is(gerr, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if gerr != nil {
				return gerr
			}
			if cur.Status == StatusLocked {
				return ErrLocked
			}
			return errors.New("narration: status transition not allowed from " + string(cur.Status))
		}
		r, err := loadScriptTx(ctx, tx, tenantID, projectID, slideID, language)
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

func loadScriptTx(ctx context.Context, tx pgx.Tx, tenantID, projectID, slideID, language string) (*Revision, error) {
	row := tx.QueryRow(ctx,
		`SELECT id, tenant_id, project_id, slide_id, language, mode, status, revision, updated_at
		 FROM narration_scripts
		 WHERE tenant_id=$1 AND project_id=$2 AND slide_id=$3 AND language=$4`,
		tenantID, projectID, slideID, language)
	var r Revision
	err := row.Scan(&r.ID, &r.TenantID, &r.ProjectID, &r.SlideID, &r.Language,
		&r.Mode, &r.Status, &r.Revision, &r.UpdatedAt)
	return &r, err
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
		`SELECT id, tenant_id, project_id, slide_id, language, mode, status, revision, updated_at
		 FROM narration_scripts WHERE id=$1`, scriptID)
	if err := row.Scan(&r.ID, &r.TenantID, &r.ProjectID, &r.SlideID, &r.Language,
		&r.Mode, &r.Status, &r.Revision, &r.UpdatedAt); err != nil {
		return nil
	}
	if segs, err := loadSegmentsTx(ctx, tx, scriptID); err == nil {
		r.Segments = segs
	}
	return &r
}
