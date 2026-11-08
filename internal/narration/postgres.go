package narration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
		meta, err := loadSegmentMetaTx(ctx, tx, scriptID)
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
				anchors = meta.Anchors[seg.SegmentID]
			}
			if anchors == nil {
				anchors = []SourceAnchor{}
			}
			// 焦点与锚点同一套「nil = 保留」语义：编辑器只改文字时不该顺手把焦点抹掉。
			// 显式清空要传一个非 nil 但 ShapeID 为空的目标（见 FocusTarget.Valid）。
			focus := seg.Focus
			if focus == nil {
				focus = meta.Focus[seg.SegmentID]
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
			var focusRaw *string
			if focus.Valid() {
				b, err := json.Marshal(focus)
				if err != nil {
					return err
				}
				s := string(b)
				focusRaw = &s
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO narration_segments (id, script_id, tenant_id, segment_id, display_text, spoken_text, source_refs, source_anchors, focus, status, revision)
				 VALUES (gen_random_uuid(), $1,$2,$3,$4,$5,$6,$7,$8,'draft',0)`,
				scriptID, tenantID, seg.SegmentID, seg.DisplayText, seg.SpokenText, refs, string(anchorBytes), focusRaw); err != nil {
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
		var collected []*Revision
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
			collected = append(collected, &r)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		// pgx 单连接不允许在结果集未关闭时发起新查询（否则 "conn busy"）；先关闭游标再加载分段。
		rows.Close()
		ids := make([]string, len(collected))
		for i, r := range collected {
			ids[i] = r.ID
		}
		byScript, serr := loadSegmentsManyTx(ctx, tx, ids)
		if serr != nil {
			return serr
		}
		for _, r := range collected {
			r.Segments = byScript[r.ID]
			revs = append(revs, r)
		}
		return nil
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
		`INSERT INTO narration_segments (id, script_id, tenant_id, segment_id, display_text, spoken_text, source_refs, source_anchors, focus, status, revision)
		 SELECT gen_random_uuid(), $1, tenant_id, segment_id, display_text, spoken_text, source_refs, source_anchors, focus, status, revision
		   FROM narration_segments WHERE script_id=$2`, newID, legacy.ID); err != nil {
		return nil, err
	}
	return loadScriptExactTx(ctx, tx, tenantID, projectID, sourceRevisionNo, slideID, language)
}

// decodeSegment 还原分段上的三个 JSON 列（source_refs / source_anchors / focus）。
func decodeSegment(seg *Segment, refs []string, anchorBytes, focusBytes []byte) error {
	seg.SourceRefs = refs
	if len(anchorBytes) > 0 {
		if err := json.Unmarshal(anchorBytes, &seg.SourceAnchors); err != nil {
			return err
		}
	}
	if len(focusBytes) > 0 {
		var f FocusTarget
		if err := json.Unmarshal(focusBytes, &f); err != nil {
			return err
		}
		seg.Focus = &f
	}
	return nil
}

// loadSegmentsTx 读取一份讲稿的全部分段（按 segment_id 升序）。
// 走批量版而不是自己写一份查询：解析逻辑只有一处，单份场景多出来的只是一次 map 分配。
func loadSegmentsTx(ctx context.Context, tx pgx.Tx, scriptID string) ([]*Segment, error) {
	byScript, err := loadSegmentsManyTx(ctx, tx, []string{scriptID})
	if err != nil {
		return nil, err
	}
	return byScript[scriptID], nil
}

// loadSegmentsManyTx 一次读完多份讲稿的分段，按 script_id 归组。
//
// 为什么要有批量版：列表场景（`ListByProject`）原本对每一页单独查一次分段，
// 一次 30 页的编辑器加载就是 1 + 30 次往返；批量版把分段读取固定成 1 次。
// 占位符按数量拼而不是 `= ANY($1)`：script_id 是 uuid，pgx 不会把 []string
// 隐式编成 uuid[]，逐个占位符既避开类型转换，也保证走 script_id 索引。
func loadSegmentsManyTx(ctx context.Context, tx pgx.Tx, scriptIDs []string) (map[string][]*Segment, error) {
	out := make(map[string][]*Segment, len(scriptIDs))
	if len(scriptIDs) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(scriptIDs))
	args := make([]any, len(scriptIDs))
	for i, id := range scriptIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	rows, err := tx.Query(ctx,
		`SELECT script_id, segment_id, display_text, spoken_text, source_refs, source_anchors, focus, status
		 FROM narration_segments WHERE script_id IN (`+strings.Join(placeholders, ",")+`)
		 ORDER BY script_id, segment_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var scriptID string
		var seg Segment
		var refs []string
		var anchorBytes, focusBytes []byte
		if err := rows.Scan(&scriptID, &seg.SegmentID, &seg.DisplayText, &seg.SpokenText,
			&refs, &anchorBytes, &focusBytes, &seg.Status); err != nil {
			return nil, err
		}
		if err := decodeSegment(&seg, refs, anchorBytes, focusBytes); err != nil {
			return nil, err
		}
		out[scriptID] = append(out[scriptID], &seg)
	}
	return out, rows.Err()
}

// segmentMeta 是更新时需要的"旧值"：调用方传 nil 表示保留，这里就是保留的来源。
type segmentMeta struct {
	Anchors map[string][]SourceAnchor
	Focus   map[string]*FocusTarget
}

func loadSegmentMetaTx(ctx context.Context, tx pgx.Tx, scriptID string) (segmentMeta, error) {
	rows, err := tx.Query(ctx, `SELECT segment_id, source_anchors, focus FROM narration_segments WHERE script_id=$1`, scriptID)
	if err != nil {
		return segmentMeta{}, err
	}
	defer rows.Close()
	meta := segmentMeta{
		Anchors: map[string][]SourceAnchor{},
		Focus:   map[string]*FocusTarget{},
	}
	for rows.Next() {
		var segmentID string
		var data []byte
		var focusBytes []byte
		if err := rows.Scan(&segmentID, &data, &focusBytes); err != nil {
			return segmentMeta{}, err
		}
		var anchors []SourceAnchor
		if len(data) > 0 {
			if err := json.Unmarshal(data, &anchors); err != nil {
				return segmentMeta{}, err
			}
		}
		meta.Anchors[segmentID] = anchors
		if len(focusBytes) > 0 {
			var f FocusTarget
			if err := json.Unmarshal(focusBytes, &f); err != nil {
				return segmentMeta{}, err
			}
			meta.Focus[segmentID] = &f
		}
	}
	return meta, rows.Err()
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
