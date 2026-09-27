package contextrule

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/F31/ppts/internal/db"
)

// SQLiteStore 是 Store 的 SQLite 实现（单租户精简 profile）。
type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(sqldb *sql.DB) *SQLiteStore { return &SQLiteStore{db: sqldb} }

var _ Store = (*SQLiteStore)(nil)

const sqCols = `id, tenant_id, pattern, replacement, priority, enabled, created_at, updated_at`

func sqScanRecord(row interface{ Scan(dest ...any) error }) (*Record, error) {
	var r Record
	var tenantID sql.NullString
	var created, updated string
	if err := row.Scan(&r.ID, &tenantID, &r.Pattern, &r.Replacement, &r.Priority, &r.Enabled, &created, &updated); err != nil {
		return nil, err
	}
	if tenantID.Valid {
		r.TenantID = tenantID.String
	}
	r.CreatedAt = db.ParseTime(created).Unix()
	r.UpdatedAt = db.ParseTime(updated).Unix()
	return &r, nil
}

func (s *SQLiteStore) ListByTenant(ctx context.Context, tenantID string) ([]*Record, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+sqCols+` FROM contextual_rules WHERE tenant_id = ? ORDER BY priority, created_at`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("contextrule list: %w", err)
	}
	defer rows.Close()
	var out []*Record
	for rows.Next() {
		r, err := sqScanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("contextrule scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) Create(ctx context.Context, rec *Record) error {
	if rec.ID == "" {
		return fmt.Errorf("contextrule create: empty id")
	}
	var tenantID any
	if rec.TenantID != "" {
		tenantID = rec.TenantID
	}
	now := db.Now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO contextual_rules (id, tenant_id, pattern, replacement, priority, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, tenantID, rec.Pattern, rec.Replacement, rec.Priority, rec.Enabled, now, now)
	if err != nil {
		return fmt.Errorf("contextrule create: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Update(ctx context.Context, rec *Record) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE contextual_rules SET pattern = ?, replacement = ?, priority = ?, enabled = ?, updated_at = ?
		 WHERE tenant_id = ? AND id = ?`,
		rec.Pattern, rec.Replacement, rec.Priority, rec.Enabled, db.Now(), rec.TenantID, rec.ID)
	if err != nil {
		return fmt.Errorf("contextrule update: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) Delete(ctx context.Context, tenantID, id string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM contextual_rules WHERE tenant_id = ? AND id = ?`, tenantID, id)
	if err != nil {
		return fmt.Errorf("contextrule delete: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// LoadAllEffective 返回租户生效规则（租户行 + 平台 NULL 行），按 priority 升序。
func (s *SQLiteStore) LoadAllEffective(ctx context.Context, tenantID string) ([]*Record, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+sqCols+` FROM contextual_rules
		 WHERE (tenant_id = ? OR tenant_id IS NULL) AND enabled = 1
		 ORDER BY priority, created_at`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("contextrule load effective: %w", err)
	}
	defer rows.Close()
	var out []*Record
	for rows.Next() {
		r, err := sqScanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("contextrule scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
