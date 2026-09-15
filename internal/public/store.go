package public

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/tenant"
)

const selectCols = `id, tenant_id, project_id, kind, status, title, summary,
  cover_object_key, sort_order, created_by, reviewed_by, reviewed_at, created_at, updated_at`

// PGStore 以 PostgreSQL 实现 Store。匿名只读方法直接走 pgxpool（不设 tenant 上下文），
// 由 RLS 的 publications_public_read 策略仅放行 status='approved'；写方法经 tenant.Run，
// RLS 要求行租户匹配调用方租户。
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore 创建公开作品存储。
func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

func (s *PGStore) Publish(ctx context.Context, tenantID string, in NewPublication) (*Publication, error) {
	return s.insert(ctx, tenantID, in, StatusPending)
}

func (s *PGStore) Feature(ctx context.Context, tenantID string, in NewPublication) (*Publication, error) {
	return s.insert(ctx, tenantID, in, StatusApproved)
}

func (s *PGStore) insert(ctx context.Context, tenantID string, in NewPublication, status Status) (*Publication, error) {
	var pub *Publication
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		pub, e = scanPublication(tx.QueryRow(ctx,
			`INSERT INTO publications (id, tenant_id, project_id, kind, status, title, summary, cover_object_key, created_by)
			 VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8)
			 RETURNING `+selectCols,
			tenantID, in.ProjectID, in.Kind, status, in.Title, in.Summary, in.CoverObjectKey, in.CreatedBy))
		return e
	})
	return pub, err
}

// ListApproved 匿名列出已批准作品。kind 为空返回两类合并；
// 游标基于 created_at（RFC3339Nano），按 created_at DESC 分页。
func (s *PGStore) ListApproved(ctx context.Context, kind Kind, cursor string, pageSize int) ([]*Publication, string, error) {
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 24
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+selectCols+`
		 FROM publications
		 WHERE status = 'approved' AND ($1 = '' OR kind = $1)
		 ORDER BY created_at DESC
		 LIMIT $2`, string(kind), pageSize+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	items, err := scanPublications(rows)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) > pageSize {
		next = items[pageSize-1].CreatedAt.Format(time.RFC3339Nano)
		items = items[:pageSize]
	}
	return items, next, nil
}

// GetApproved 匿名获取单条已批准作品。
func (s *PGStore) GetApproved(ctx context.Context, id string) (*Publication, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+selectCols+` FROM publications WHERE id = $1 AND status = 'approved'`, id)
	pub, err := scanPublication(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return pub, err
}

// ListByProject 列出某租户某项目下的全部发布（含未批准）。
func (s *PGStore) ListByProject(ctx context.Context, tenantID, projectID string) ([]*Publication, error) {
	var items []*Publication
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx,
			`SELECT `+selectCols+` FROM publications WHERE tenant_id = $1 AND project_id = $2 ORDER BY created_at DESC`,
			tenantID, projectID)
		if e != nil {
			return e
		}
		defer rows.Close()
		items, e = scanPublications(rows)
		return e
	})
	return items, err
}

// Review 由管理员审核：approve=true 置 approved，否则置 rejected。
func (s *PGStore) Review(ctx context.Context, tenantID, id string, approve bool, reviewer string) (*Publication, error) {
	var pub *Publication
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		pub, e = scanPublication(tx.QueryRow(ctx,
			`SELECT `+selectCols+` FROM publications WHERE id = $1 AND tenant_id = $2`, id, tenantID))
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		newStatus := StatusRejected
		if approve {
			newStatus = StatusApproved
		}
		if _, e := tx.Exec(ctx,
			`UPDATE publications SET status = $1, reviewed_by = $2, reviewed_at = now(), updated_at = now()
			 WHERE id = $3 AND tenant_id = $4`, newStatus, reviewer, id, tenantID); e != nil {
			return e
		}
		pub.Status = newStatus
		pub.ReviewedBy = reviewer
		pub.ReviewedAt = time.Now()
		return nil
	})
	return pub, err
}

// Delete 由管理员删除公开作品。
func (s *PGStore) Delete(ctx context.Context, tenantID, id string) error {
	return tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `DELETE FROM publications WHERE id = $1 AND tenant_id = $2`, id, tenantID)
		if e != nil {
			return e
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func scanPublication(row pgx.Row) (*Publication, error) {
	var p Publication
	err := row.Scan(&p.ID, &p.TenantID, &p.ProjectID, &p.Kind, &p.Status, &p.Title, &p.Summary,
		&p.CoverObjectKey, &p.SortOrder, &p.CreatedBy, &p.ReviewedBy, &p.ReviewedAt, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func scanPublications(rows pgx.Rows) ([]*Publication, error) {
	out := make([]*Publication, 0, 8)
	for rows.Next() {
		p, err := scanPublication(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
