package pronunciation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("pronunciation dictionary not found")

// Dictionary 是发音词典的领域模型。
type Dictionary struct {
	ID        string `json:"id"`
	TenantID  string `json:"tenantId"`
	Name      string `json:"name"`
	Rules     Rules  `json:"rules"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

// Store 管理发音词典的持久化。
type Store interface {
	ListByTenant(ctx context.Context, tenantID string) ([]*Dictionary, error)
	GetByID(ctx context.Context, tenantID, id string) (*Dictionary, error)
	Create(ctx context.Context, dict *Dictionary) error
	Update(ctx context.Context, dict *Dictionary) error
	Delete(ctx context.Context, tenantID, id string) error
	// LoadTenantDefault 加载租户默认发音词典规则（按 created_at 取最新）。
	// 未配置词典时返回空集，不报错。
	LoadTenantDefault(ctx context.Context, tenantID string) (Rules, error)
}

type PGStore struct {
	pool *pgxpool.Pool
}

func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

func (s *PGStore) ListByTenant(ctx context.Context, tenantID string) ([]*Dictionary, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, tenant_id, name, rules, EXTRACT(EPOCH FROM created_at)::bigint, EXTRACT(EPOCH FROM updated_at)::bigint
		 FROM pronunciation_dictionaries WHERE tenant_id = $1 ORDER BY created_at`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("pronunciation list: %w", err)
	}
	defer rows.Close()
	var dicts []*Dictionary
	for rows.Next() {
		var d Dictionary
		var rulesRaw json.RawMessage
		if err := rows.Scan(&d.ID, &d.TenantID, &d.Name, &rulesRaw, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, fmt.Errorf("pronunciation scan: %w", err)
		}
		d.Rules = ParseRules(rulesRaw)
		dicts = append(dicts, &d)
	}
	return dicts, rows.Err()
}

func (s *PGStore) GetByID(ctx context.Context, tenantID, id string) (*Dictionary, error) {
	var d Dictionary
	var rulesRaw json.RawMessage
	err := s.pool.QueryRow(ctx,
		`SELECT id, tenant_id, name, rules, EXTRACT(EPOCH FROM created_at)::bigint, EXTRACT(EPOCH FROM updated_at)::bigint
		 FROM pronunciation_dictionaries WHERE tenant_id = $1 AND id = $2`, tenantID, id).
		Scan(&d.ID, &d.TenantID, &d.Name, &rulesRaw, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("pronunciation get: %w", err)
	}
	d.Rules = ParseRules(rulesRaw)
	return &d, nil
}

func (s *PGStore) Create(ctx context.Context, dict *Dictionary) error {
	rulesJSON, err := dict.Rules.Marshal()
	if err != nil {
		return fmt.Errorf("pronunciation marshal rules: %w", err)
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO pronunciation_dictionaries (id, tenant_id, name, rules) VALUES ($1, $2, $3, $4)`,
		dict.ID, dict.TenantID, dict.Name, rulesJSON)
	if err != nil {
		return fmt.Errorf("pronunciation create: %w", err)
	}
	return nil
}

func (s *PGStore) Update(ctx context.Context, dict *Dictionary) error {
	rulesJSON, err := dict.Rules.Marshal()
	if err != nil {
		return fmt.Errorf("pronunciation marshal rules: %w", err)
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE pronunciation_dictionaries SET name = $3, rules = $4, updated_at = now()
		 WHERE tenant_id = $1 AND id = $2`, dict.TenantID, dict.ID, dict.Name, rulesJSON)
	if err != nil {
		return fmt.Errorf("pronunciation update: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) Delete(ctx context.Context, tenantID, id string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM pronunciation_dictionaries WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return fmt.Errorf("pronunciation delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// LoadTenantDefault 返回租户最新的发音词典规则；无词典时返回空集。
func (s *PGStore) LoadTenantDefault(ctx context.Context, tenantID string) (Rules, error) {
	var rulesRaw json.RawMessage
	err := s.pool.QueryRow(ctx,
		`SELECT rules FROM pronunciation_dictionaries
		 WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT 1`, tenantID).Scan(&rulesRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pronunciation load default: %w", err)
	}
	return ParseRules(rulesRaw), nil
}
