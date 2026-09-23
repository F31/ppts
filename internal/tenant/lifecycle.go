package tenant

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Status 是租户生命周期状态。
type Status string

const (
	StatusActive    Status = "active"
	StatusSuspended Status = "suspended"
	StatusDeleted   Status = "deleted"
)

// Status 返回租户生命周期状态；不存在返回 ErrTenantNotFound。
func (s *PGStore) Status(ctx context.Context, tenantID string) (Status, error) {
	var status string
	err := s.pool.QueryRow(ctx, "SELECT status FROM tenants WHERE id=$1", tenantID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrTenantNotFound
	}
	if err != nil {
		return "", err
	}
	return Status(status), nil
}

// TenantActive 供 API 身份中间件检查租户是否可服务。
func (s *PGStore) TenantActive(ctx context.Context, tenantID string) (bool, error) {
	status, err := s.Status(ctx, tenantID)
	if err != nil {
		return false, err
	}
	return status == StatusActive, nil
}

// Suspend 停用租户。
func (s *PGStore) Suspend(ctx context.Context, tenantID string) error {
	tag, err := s.pool.Exec(ctx,
		"UPDATE tenants SET status='suspended', suspended_at=now(), updated_at=now() WHERE id=$1", tenantID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrTenantNotFound
	}
	return nil
}

// Resume 恢复租户。
func (s *PGStore) Resume(ctx context.Context, tenantID string) error {
	tag, err := s.pool.Exec(ctx,
		"UPDATE tenants SET status='active', suspended_at=NULL, updated_at=now() WHERE id=$1", tenantID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrTenantNotFound
	}
	return nil
}

// Summary 是运营商后台所需的租户概览行。
type Summary struct {
	ID        string
	Name      string
	Type      string
	Status    Status
	CreatedAt time.Time
}

// ListTenants 返回租户概览（按创建时间倒序，最多 limit 条）。
func (s *PGStore) ListTenants(ctx context.Context, limit int) ([]Summary, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id::text, name, type, status, created_at FROM tenants ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Summary
	for rows.Next() {
		var sm Summary
		var status string
		if err := rows.Scan(&sm.ID, &sm.Name, &sm.Type, &status, &sm.CreatedAt); err != nil {
			return nil, err
		}
		sm.Status = Status(status)
		out = append(out, sm)
	}
	return out, rows.Err()
}
