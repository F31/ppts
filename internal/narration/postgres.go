package narration

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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
	rev, err := s.loadScript(ctx, tenantID, projectID, slideID, language)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	segs, err := s.loadSegments(ctx, rev.ID)
	if err != nil {
		return nil, err
	}
	rev.Segments = segs
	return rev, nil
}

func (s *PGStore) EnsureExists(ctx context.Context, tenantID, projectID, slideID, language string, mode ScriptMode) (*Revision, error) {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO narration_scripts (id, tenant_id, project_id, slide_id, language, mode)
		 VALUES (gen_random_uuid(), $1,$2,$3,$4,$5)
		 ON CONFLICT (tenant_id, project_id, slide_id, language) DO NOTHING`,
		tenantID, projectID, slideID, language, string(mode))
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, tenantID, projectID, slideID, language)
}

func (s *PGStore) Update(ctx context.Context, tenantID, projectID, slideID, language string, expected int64, segments []*Segment) (*Revision, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())

	var scriptID string
	var status ScriptStatus
	var revision int64
	err = tx.QueryRow(ctx,
		`SELECT id, status, revision FROM narration_scripts
		 WHERE tenant_id=$1 AND project_id=$2 AND slide_id=$3 AND language=$4
		 FOR UPDATE`, tenantID, projectID, slideID, language).
		Scan(&scriptID, &status, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if status == StatusLocked {
		return nil, ErrLocked
	}
	if revision != expected {
		latest := s.loadFromTx(ctx, scriptID)
		return nil, &ErrConflict{Latest: latest}
	}
	// 整页替换分段（分段携带稳定 ID；语音/字幕按稳定 ID 复用）。
	if _, err := tx.Exec(ctx, `DELETE FROM narration_segments WHERE script_id=$1`, scriptID); err != nil {
		return nil, err
	}
	for _, seg := range segments {
		if _, err := tx.Exec(ctx,
			`INSERT INTO narration_segments (id, script_id, tenant_id, segment_id, display_text, spoken_text, source_refs, status, revision)
			 VALUES (gen_random_uuid(), $1,$2,$3,$4,$5,$6,'draft',0)`,
			scriptID, tenantID, seg.SegmentID, seg.DisplayText, seg.SpokenText, seg.SourceRefs); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx,
		`UPDATE narration_scripts SET revision=revision+1, status='draft', updated_at=now() WHERE id=$1`, scriptID); err != nil {
		return nil, err
	}
	if err := tx.Commit(context.Background()); err != nil {
		return nil, err
	}
	return s.Get(ctx, tenantID, projectID, slideID, language)
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
	tag, err := s.pool.Exec(ctx,
		`UPDATE narration_scripts SET status=$5, updated_at=now()
		 WHERE tenant_id=$1 AND project_id=$2 AND slide_id=$3 AND language=$4 AND status=$6`,
		tenantID, projectID, slideID, language, string(newStatus), allowFrom)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		// 目标可能不存在或状态不允许。
		rev, gerr := s.Get(ctx, tenantID, projectID, slideID, language)
		if errors.Is(gerr, ErrNotFound) {
			return nil, ErrNotFound
		}
		if gerr != nil {
			return nil, gerr
		}
		if rev.Status == StatusLocked {
			return nil, ErrLocked
		}
		return nil, errors.New("narration: status transition not allowed from " + string(rev.Status))
	}
	return s.Get(ctx, tenantID, projectID, slideID, language)
}

func (s *PGStore) loadScript(ctx context.Context, tenantID, projectID, slideID, language string) (*Revision, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT id, tenant_id, project_id, slide_id, language, mode, status, revision, updated_at
		 FROM narration_scripts
		 WHERE tenant_id=$1 AND project_id=$2 AND slide_id=$3 AND language=$4`,
		tenantID, projectID, slideID, language)
	var r Revision
	err := row.Scan(&r.ID, &r.TenantID, &r.ProjectID, &r.SlideID, &r.Language,
		&r.Mode, &r.Status, &r.Revision, &r.UpdatedAt)
	return &r, err
}

func (s *PGStore) loadSegments(ctx context.Context, scriptID string) ([]*Segment, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT segment_id, display_text, spoken_text, source_refs, status
		 FROM narration_segments WHERE script_id=$1 ORDER BY segment_id`, scriptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var segs []*Segment
	for rows.Next() {
		var seg Segment
		var refs []string
		if err := rows.Scan(&seg.SegmentID, &seg.DisplayText, &seg.SpokenText, &refs, &seg.Status); err != nil {
			return nil, err
		}
		seg.SourceRefs = refs
		segs = append(segs, &seg)
	}
	return segs, rows.Err()
}

// loadFromTx 在回滚后仍返回最新完整讲稿（供 ErrConflict.Latest）。
func (s *PGStore) loadFromTx(ctx context.Context, scriptID string) *Revision {
	var r Revision
	row := s.pool.QueryRow(ctx,
		`SELECT id, tenant_id, project_id, slide_id, language, mode, status, revision, updated_at
		 FROM narration_scripts WHERE id=$1`, scriptID)
	if err := row.Scan(&r.ID, &r.TenantID, &r.ProjectID, &r.SlideID, &r.Language,
		&r.Mode, &r.Status, &r.Revision, &r.UpdatedAt); err != nil {
		return nil
	}
	if segs, err := s.loadSegments(ctx, scriptID); err == nil {
		r.Segments = segs
	}
	return &r
}
