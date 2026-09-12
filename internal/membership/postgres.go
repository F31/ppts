package membership

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/tenant"
)

// PGStore 以 PostgreSQL 实现成员存储。
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore 创建成员存储。
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

// GetRole 返回成员角色；不存在返回 ErrNotFound。
func (s *PGStore) GetRole(ctx context.Context, tenantID, userID string) (Role, error) {
	var role string
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			"SELECT role FROM tenant_members WHERE tenant_id=$1 AND user_id=$2",
			tenantID, userID).Scan(&role)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return Role(role), nil
}

// List 返回租户全部成员。
func (s *PGStore) List(ctx context.Context, tenantID string) ([]Member, error) {
	var out []Member
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			"SELECT user_id, role FROM tenant_members WHERE tenant_id=$1 ORDER BY created_at", tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m Member
			if err := rows.Scan(&m.UserID, (*string)(&m.Role)); err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SetRole 新增或更新成员角色。
func (s *PGStore) SetRole(ctx context.Context, tenantID, userID string, role Role) error {
	tenantID = strings.TrimSpace(tenantID)
	userID = strings.TrimSpace(userID)
	if tenantID == "" || userID == "" {
		return errors.New("membership: tenant_id and user_id are required")
	}
	if !Valid(role) {
		return errors.New("membership: invalid role")
	}
	return tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO tenant_members (tenant_id, user_id, role)
			 VALUES ($1,$2,$3)
			 ON CONFLICT (tenant_id, user_id) DO UPDATE
			   SET role=EXCLUDED.role, updated_at=now()`,
			tenantID, userID, string(role))
		return err
	})
}

// Remove 移除成员；不存在返回 ErrNotFound。
func (s *PGStore) Remove(ctx context.Context, tenantID, userID string) error {
	return tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			"DELETE FROM tenant_members WHERE tenant_id=$1 AND user_id=$2", tenantID, userID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}
