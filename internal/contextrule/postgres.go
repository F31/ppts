package contextrule

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore 是 Store 的 PostgreSQL 实现。
type PGStore struct {
	pool *pgxpool.Pool
}

func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

var _ Store = (*PGStore)(nil)

const pgCols = `id, tenant_id, pattern, replacement, priority, enabled, EXTRACT(EPOCH FROM created_at)::bigint, EXTRACT(EPOCH FROM updated_at)::bigint`

func pgScanRecord(row interface{ Scan(dest ...any) error }) (*Record, error) {
	var r Record
	var tenantID *string
	if err := row.Scan(&r.ID, &tenantID, &r.Pattern, &r.Replacement, &r.Priority, &r.Enabled, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	if tenantID != nil {
		r.TenantID = *tenantID
	}
	return &r, nil
}

func (s *PGStore) ListByTenant(ctx context.Context, tenantID string) ([]*Record, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+pgCols+` FROM contextual_rules WHERE tenant_id = $1 ORDER BY priority, created_at`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("contextrule list: %w", err)
	}
	defer rows.Close()
	var out []*Record
	for rows.Next() {
		r, err := pgScanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("contextrule scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *PGStore) Create(ctx context.Context, rec *Record) error {
	if rec.ID == "" {
		return fmt.Errorf("contextrule create: empty id")
	}
	var tenantID any
	if rec.TenantID != "" {
		tenantID = rec.TenantID
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO contextual_rules (id, tenant_id, pattern, replacement, priority, enabled)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		rec.ID, tenantID, rec.Pattern, rec.Replacement, rec.Priority, rec.Enabled)
	if err != nil {
		return fmt.Errorf("contextrule create: %w", err)
	}
	return nil
}

func (s *PGStore) Update(ctx context.Context, rec *Record) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE contextual_rules SET pattern = $3, replacement = $4, priority = $5, enabled = $6, updated_at = now()
		 WHERE tenant_id = $1 AND id = $2`,
		rec.TenantID, rec.ID, rec.Pattern, rec.Replacement, rec.Priority, rec.Enabled)
	if err != nil {
		return fmt.Errorf("contextrule update: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) Delete(ctx context.Context, tenantID, id string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM contextual_rules WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return fmt.Errorf("contextrule delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// LoadAllEffective 返回租户生效规则（租户行 + 平台 NULL 行），按 priority 升序。
func (s *PGStore) LoadAllEffective(ctx context.Context, tenantID string) ([]*Record, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+pgCols+` FROM contextual_rules
		 WHERE (tenant_id = $1 OR tenant_id IS NULL) AND enabled = true
		 ORDER BY priority, created_at`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("contextrule load effective: %w", err)
	}
	defer rows.Close()
	var out []*Record
	for rows.Next() {
		r, err := pgScanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("contextrule scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
