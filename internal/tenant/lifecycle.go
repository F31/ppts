package tenant

import (
	"context"
	"errors"

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
