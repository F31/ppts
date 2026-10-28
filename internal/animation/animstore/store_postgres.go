package animstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/tenant"
)

// PGStore 以 PostgreSQL 实现 Store。所有读写都经 tenant.Run 设置事务局部
// app.tenant_id，RLS 策略据此判定；漏设即拒绝（与 narration 一致）。
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore 创建存储。
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

var _ Store = (*PGStore)(nil)

// 用 ::text 显式转换，避免 pgx 对 uuid 的扫描类型差异（sqlite 侧本来就是 string）。
const pgAnimationColumns = `id::text, tenant_id::text, project_id::text, slide_id, language,
	source_revision_no, revision, status, preset_id, intensity, tracks, rendered_revision, updated_at`

func pgScanRevision(row pgx.Row) (*animation.Revision, error) {
	var (
		r      animation.Revision
		status string
		tracks []byte
	)
	if err := row.Scan(&r.ID, &r.TenantID, &r.ProjectID, &r.SlideID, &r.Language,
		&r.SourceRevisionNo, &r.Revision, &status, &r.PresetID, &r.Intensity,
		&tracks, &r.RenderedRevision, &r.UpdatedAt); err != nil {
		return nil, err
	}
	parsed, err := unmarshalTracks(tracks)
	if err != nil {
		return nil, err
	}
	r.Status = animation.Status(status)
	r.Tracks = parsed
	return &r, nil
}

func pgLoad(ctx context.Context, q pgx.Tx, key animation.Key) (*animation.Revision, error) {
	row := q.QueryRow(ctx,
		`SELECT `+pgAnimationColumns+` FROM animation_revisions
		 WHERE tenant_id = $1::uuid AND project_id = $2::uuid AND slide_id = $3 AND language = $4
		   AND source_revision_no IN ($5, 0)
		 ORDER BY source_revision_no DESC LIMIT 1`,
		key.TenantID, key.ProjectID, key.SlideID, key.Language, key.SourceRevisionNo)
	rev, err := pgScanRevision(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, animation.ErrNotFound
	}
	return rev, err
}

func pgLoadExact(ctx context.Context, q pgx.Tx, key animation.Key) (*animation.Revision, error) {
	row := q.QueryRow(ctx,
		`SELECT `+pgAnimationColumns+` FROM animation_revisions
		 WHERE tenant_id = $1::uuid AND project_id = $2::uuid AND slide_id = $3 AND language = $4 AND source_revision_no = $5`,
		key.TenantID, key.ProjectID, key.SlideID, key.Language, key.SourceRevisionNo)
	rev, err := pgScanRevision(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, animation.ErrNotFound
	}
	return rev, err
}

// Get 实现 Store。
func (s *PGStore) Get(ctx context.Context, key animation.Key) (*animation.Revision, error) {
	key = key.Normalize()
	if err := key.Validate(); err != nil {
		return nil, err
	}
	var rev *animation.Revision
	err := tenant.Run(ctx, s.pool, key.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		r, err := pgLoad(ctx, tx, key)
		if err != nil {
			return err
		}
		rev = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rev, nil
}

// Put 实现 Store。
func (s *PGStore) Put(ctx context.Context, rev *animation.Revision, expected int64) (*animation.Revision, error) {
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

	var saved *animation.Revision
	err = tenant.Run(ctx, s.pool, key.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		cur, err := pgLoadExact(ctx, tx, key)
		switch {
		case errors.Is(err, animation.ErrNotFound):
			if expected != 0 {
				return &animation.ErrConflict{}
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO animation_revisions (tenant_id, project_id, slide_id, language,
					source_revision_no, revision, status, preset_id, intensity, tracks)
				 VALUES ($1::uuid,$2::uuid,$3,$4,$5,1,$6,$7,$8,$9::jsonb)`,
				key.TenantID, key.ProjectID, key.SlideID, key.Language, key.SourceRevisionNo,
				string(animation.StatusDraft), rev.PresetID, rev.Intensity, string(tracks)); err != nil {
				return err
			}
			out, err := pgLoadExact(ctx, tx, key)
			if err != nil {
				return err
			}
			saved = out
			return nil
		case err != nil:
			return err
		}

		if cur.Status == animation.StatusLocked {
			return animation.ErrLocked
		}
		if cur.Revision != expected {
			return &animation.ErrConflict{Latest: cur}
		}

		tag, err := tx.Exec(ctx,
			`UPDATE animation_revisions
			 SET revision = revision + 1, preset_id = $1, intensity = $2, tracks = $3::jsonb, updated_at = now()
			 WHERE id = $4::uuid AND revision = $5`,
			rev.PresetID, rev.Intensity, string(tracks), cur.ID, expected)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			latest, lerr := pgLoadExact(ctx, tx, key)
			if lerr != nil {
				return lerr
			}
			return &animation.ErrConflict{Latest: latest}
		}

		out, err := pgLoadExact(ctx, tx, key)
		if err != nil {
			return err
		}
		saved = out
		return nil
	})
	if err != nil {
		return nil, err
	}
	return saved, nil
}

// SetStatus 实现 Store。
func (s *PGStore) SetStatus(ctx context.Context, key animation.Key, target animation.Status) (*animation.Revision, error) {
	key = key.Normalize()
	if err := key.Validate(); err != nil {
		return nil, err
	}
	var allowedFrom animation.Status
	switch target {
	case animation.StatusApproved:
		allowedFrom = animation.StatusLocked
	case animation.StatusLocked:
		allowedFrom = animation.StatusApproved
	default:
		return nil, fmt.Errorf("%w: unknown target %q", animation.ErrStatusTransition, target)
	}

	var saved *animation.Revision
	err := tenant.Run(ctx, s.pool, key.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		cur, err := pgLoadExact(ctx, tx, key)
		if err != nil {
			return err
		}
		if !animation.AllowedTransition(cur.Status, target) {
			if cur.Status == animation.StatusLocked {
				return animation.ErrLocked
			}
			return fmt.Errorf("%w: %s → %s", animation.ErrStatusTransition, cur.Status, target)
		}

		tag, err := tx.Exec(ctx,
			`UPDATE animation_revisions SET status = $1, updated_at = now()
			 WHERE id = $2::uuid AND status IN ($3, $4)`,
			string(target), cur.ID, string(allowedFrom), string(animation.StatusDraft))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			latest, lerr := pgLoadExact(ctx, tx, key)
			if lerr != nil {
				return lerr
			}
			return &animation.ErrConflict{Latest: latest}
		}

		out, err := pgLoadExact(ctx, tx, key)
		if err != nil {
			return err
		}
		saved = out
		return nil
	})
	if err != nil {
		return nil, err
	}
	return saved, nil
}
