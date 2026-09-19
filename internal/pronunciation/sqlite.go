package pronunciation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/F31/ppts/internal/db"
)

// SQLiteStore 是 Store 的 SQLite 实现（单租户精简 profile）。
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore 创建 SQLite 发音词典存储。
func NewSQLiteStore(sqldb *sql.DB) *SQLiteStore { return &SQLiteStore{db: sqldb} }

var _ Store = (*SQLiteStore)(nil)

const sqDictColumns = `id, tenant_id, name, rules, created_at, updated_at`

func sqScanDict(row rowScanner) (*Dictionary, error) {
	var d Dictionary
	var rulesRaw string
	var created, updated string
	if err := row.Scan(&d.ID, &d.TenantID, &d.Name, &rulesRaw, &created, &updated); err != nil {
		return nil, err
	}
	d.Rules = ParseRules(json.RawMessage(rulesRaw))
	d.CreatedAt = db.ParseTime(created).Unix()
	d.UpdatedAt = db.ParseTime(updated).Unix()
	return &d, nil
}

type rowScanner interface{ Scan(dest ...any) error }

func (s *SQLiteStore) ListByTenant(ctx context.Context, tenantID string) ([]*Dictionary, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+sqDictColumns+` FROM pronunciation_dictionaries
		 WHERE tenant_id = ? ORDER BY created_at`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("pronunciation list: %w", err)
	}
	defer rows.Close()
	var dicts []*Dictionary
	for rows.Next() {
		d, err := sqScanDict(rows)
		if err != nil {
			return nil, fmt.Errorf("pronunciation scan: %w", err)
		}
		dicts = append(dicts, d)
	}
	return dicts, rows.Err()
}

func (s *SQLiteStore) GetByID(ctx context.Context, tenantID, id string) (*Dictionary, error) {
	d, err := sqScanDict(s.db.QueryRowContext(ctx,
		`SELECT `+sqDictColumns+` FROM pronunciation_dictionaries WHERE tenant_id = ? AND id = ?`,
		tenantID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("pronunciation get: %w", err)
	}
	return d, nil
}

func (s *SQLiteStore) Create(ctx context.Context, dict *Dictionary) error {
	rulesJSON, err := dict.Rules.Marshal()
	if err != nil {
		return fmt.Errorf("pronunciation marshal rules: %w", err)
	}
	now := db.Now()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO pronunciation_dictionaries (id, tenant_id, name, rules, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		dict.ID, dict.TenantID, dict.Name, string(rulesJSON), now, now); err != nil {
		return fmt.Errorf("pronunciation create: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Update(ctx context.Context, dict *Dictionary) error {
	rulesJSON, err := dict.Rules.Marshal()
	if err != nil {
		return fmt.Errorf("pronunciation marshal rules: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE pronunciation_dictionaries SET name = ?, rules = ?, updated_at = ?
		 WHERE tenant_id = ? AND id = ?`,
		dict.Name, string(rulesJSON), db.Now(), dict.TenantID, dict.ID)
	if err != nil {
		return fmt.Errorf("pronunciation update: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) Delete(ctx context.Context, tenantID, id string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM pronunciation_dictionaries WHERE tenant_id = ? AND id = ?`, tenantID, id)
	if err != nil {
		return fmt.Errorf("pronunciation delete: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// LoadTenantDefault 返回租户最新的发音词典规则；无词典时返回空集。
func (s *SQLiteStore) LoadTenantDefault(ctx context.Context, tenantID string) (Rules, error) {
	var rulesRaw string
	err := s.db.QueryRowContext(ctx,
		`SELECT rules FROM pronunciation_dictionaries
		 WHERE tenant_id = ? ORDER BY created_at DESC LIMIT 1`, tenantID).Scan(&rulesRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pronunciation load default: %w", err)
	}
	return ParseRules(json.RawMessage(rulesRaw)), nil
}
