package tenant

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrTenantNotFound 表示租户不存在。
var ErrTenantNotFound = errors.New("tenant: not found")

// Policy 是租户策略（持久化于 tenants.policy jsonb，V4.0 §12.4）。
type Policy struct {
	StorageBackend           string `json:"storage_backend"`
	StorageRegion            string `json:"storage_region"`
	SourceRetentionDays      int    `json:"source_retention_days"`
	StorageTransitionDays    int    `json:"storage_transition_days"`
	StorageExpirationDays    int    `json:"storage_expiration_days"`
	EnvelopeEncryption       bool   `json:"envelope_encryption"`
	DeleteSourceAfterDefault bool   `json:"delete_source_after_default"`
	MaxConcurrentJobs        int    `json:"max_concurrent_jobs"`
	MaxStorageBytes          int64  `json:"max_storage_bytes"`
}

// LifecyclePolicySetting 是租户级存储生命周期策略下发所需的控制面行。
type LifecyclePolicySetting struct {
	TenantID string
	Policy   Policy
}

// PGStore 读取控制面租户策略（tenants 表不在 RLS 保护范围）。
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore 创建存储。
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

// GetPolicy 读取租户策略；不存在返回 ErrTenantNotFound。
func (s *PGStore) GetPolicy(ctx context.Context, tenantID string) (*Policy, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, "SELECT policy FROM tenants WHERE id=$1", tenantID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTenantNotFound
	}
	if err != nil {
		return nil, err
	}
	p := &Policy{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, p); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// ObjectStoreBackend 返回租户对象存储后端名称，供 objectstore.Registry 路由使用。
func (s *PGStore) ObjectStoreBackend(ctx context.Context, tenantID string) (string, error) {
	p, err := s.GetPolicy(ctx, tenantID)
	if err != nil {
		return "", err
	}
	return p.StorageBackend, nil
}

// ObjectEnvelopeEncryption reports whether object payloads for tenant should be envelope-encrypted.
func (s *PGStore) ObjectEnvelopeEncryption(ctx context.Context, tenantID string) (bool, error) {
	p, err := s.GetPolicy(ctx, tenantID)
	if err != nil {
		return false, err
	}
	return p.EnvelopeEncryption, nil
}

// ListLifecyclePolicies 返回 active 租户的存储生命周期策略配置。
func (s *PGStore) ListLifecyclePolicies(ctx context.Context) ([]LifecyclePolicySetting, error) {
	rows, err := s.pool.Query(ctx, "SELECT id, policy FROM tenants WHERE status='active' ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LifecyclePolicySetting
	for rows.Next() {
		var setting LifecyclePolicySetting
		var raw []byte
		if err := rows.Scan(&setting.TenantID, &raw); err != nil {
			return nil, err
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &setting.Policy); err != nil {
				return nil, err
			}
		}
		out = append(out, setting)
	}
	return out, rows.Err()
}

// SetPolicy 写入租户策略（管理用途）。
func (s *PGStore) SetPolicy(ctx context.Context, tenantID string, policy Policy) error {
	raw, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, "UPDATE tenants SET policy=$2 WHERE id=$1", tenantID, raw)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrTenantNotFound
	}
	return nil
}
