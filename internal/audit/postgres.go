package audit

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/tenant"
)

// PGStore 以 PostgreSQL 实现审计存储。
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore 创建审计存储。
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

const eventColumns = `id::text, tenant_id::text, actor_user, action, resource_type, resource_id, metadata, created_at`

// Record 在租户事务内追加一条审计事件。
func (s *PGStore) Record(ctx context.Context, e Event) error {
	if strings.TrimSpace(e.TenantID) == "" {
		return ErrTenantRequired
	}
	if strings.TrimSpace(e.Action) == "" {
		return ErrActionRequired
	}
	metadata := e.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	return tenant.Run(ctx, s.pool, e.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO audit_events (tenant_id, actor_user, action, resource_type, resource_id, metadata)
			 VALUES ($1,$2,$3,$4,$5,$6)`,
			e.TenantID, e.ActorUser, e.Action, e.ResourceType, e.ResourceID, raw)
		return err
	})
}

// List 按租户查询审计事件（created_at 倒序）。
func (s *PGStore) List(ctx context.Context, tenantID string, filter Filter) ([]Event, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, ErrTenantRequired
	}
	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var out []Event
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		args := []any{tenantID, limit}
		where := "tenant_id=$1"
		if filter.Action != "" {
			args = append(args, filter.Action)
			where += " AND action=$" + strconv.Itoa(len(args))
		}
		if filter.ResourceType != "" {
			args = append(args, filter.ResourceType)
			where += " AND resource_type=$" + strconv.Itoa(len(args))
		}
		if !filter.Since.IsZero() {
			args = append(args, filter.Since)
			where += " AND created_at >= $" + strconv.Itoa(len(args))
		}
		if !filter.Before.IsZero() {
			args = append(args, filter.Before)
			where += " AND created_at < $" + strconv.Itoa(len(args))
		}
		rows, err := tx.Query(ctx,
			"SELECT "+eventColumns+" FROM audit_events WHERE "+where+" ORDER BY created_at DESC LIMIT $2", args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanEvent(rows)
			if err != nil {
				return err
			}
			out = append(out, *e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func scanEvent(row pgx.Row) (*Event, error) {
	var e Event
	var raw []byte
	if err := row.Scan(&e.ID, &e.TenantID, &e.ActorUser, &e.Action, &e.ResourceType, &e.ResourceID, &raw, &e.CreatedAt); err != nil {
		return nil, err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &e.Metadata); err != nil {
			return nil, err
		}
	}
	return &e, nil
}

// DeleteBefore 删除 before 之前（排他）的审计事件，返回删除行数。
func (s *PGStore) DeleteBefore(ctx context.Context, tenantID string, before time.Time) (int64, error) {
	if strings.TrimSpace(tenantID) == "" {
		return 0, ErrTenantRequired
	}
	var n int64
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			"DELETE FROM audit_events WHERE tenant_id=$1 AND created_at < $2", tenantID, before)
		if err != nil {
			return err
		}
		n = tag.RowsAffected()
		return nil
	})
	return n, err
}
