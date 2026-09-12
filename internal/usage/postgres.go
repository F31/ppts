package usage

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/tenant"
)

// PGStore 以 PostgreSQL 实现 Store。所有写操作在租户事务内、以行锁与条件更新保证原子。
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore 创建存储。
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

const reservationColumns = `id, tenant_id, logical_operation_id, usage_kind, reserved_units, state, created_at, updated_at`

func scanReservation(row pgx.Row) (*Reservation, error) {
	var r Reservation
	var kind, state string
	if err := row.Scan(&r.ID, &r.TenantID, &r.LogicalOperationID, &kind, &r.ReservedUnits, &state, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	r.Kind, r.State = Kind(kind), state
	return &r, nil
}

// Reserve 原子预占。首次写入配额行默认不限量（limit_units=-1），由 SetLimit 收敛上限。
func (s *PGStore) Reserve(ctx context.Context, tenantID, logicalOperationID string, kind Kind, units float64) (*Reservation, error) {
	if units < 0 {
		units = 0
	}
	var out *Reservation
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO tenant_quotas (tenant_id, usage_kind, limit_units) VALUES ($1,$2,-1)
			 ON CONFLICT (tenant_id, usage_kind) DO NOTHING`,
			tenantID, string(kind)); err != nil {
			return err
		}
		// 幂等：同一逻辑操作只预占一次。
		r, err := scanReservation(tx.QueryRow(ctx,
			`INSERT INTO quota_reservations (id, tenant_id, logical_operation_id, usage_kind, reserved_units)
			 VALUES (gen_random_uuid(), $1,$2,$3,$4)
			 ON CONFLICT (tenant_id, logical_operation_id, usage_kind) DO NOTHING
			 RETURNING `+reservationColumns,
			tenantID, logicalOperationID, string(kind), units))
		if errors.Is(err, pgx.ErrNoRows) {
			// 已存在：返回既有预占（不重复计入）。
			r, err = scanReservation(tx.QueryRow(ctx,
				`SELECT `+reservationColumns+` FROM quota_reservations
				 WHERE tenant_id=$1 AND logical_operation_id=$2 AND usage_kind=$3`,
				tenantID, logicalOperationID, string(kind)))
			if err != nil {
				return err
			}
			out = r
			return nil
		}
		if err != nil {
			return err
		}
		// 原子条件更新：不限量或剩余额度足够才计入。
		tag, err := tx.Exec(ctx,
			`UPDATE tenant_quotas SET reserved_units=reserved_units+$3, updated_at=now()
			 WHERE tenant_id=$1 AND usage_kind=$2
			   AND (limit_units < 0 OR reserved_units + consumed_units + $3 <= limit_units)`,
			tenantID, string(kind), units)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrInsufficientQuota
		}
		r.Created = true
		out = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Settle 结算预占并写账本。重复结算幂等（账本唯一键 + 状态检查）。
func (s *PGStore) Settle(ctx context.Context, tenantID, logicalOperationID string, kind Kind, actualUnits float64, priceVersion string) error {
	if actualUnits < 0 {
		actualUnits = 0
	}
	return tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		res, err := scanReservation(tx.QueryRow(ctx,
			`SELECT `+reservationColumns+` FROM quota_reservations
			 WHERE tenant_id=$1 AND logical_operation_id=$2 AND usage_kind=$3 FOR UPDATE`,
			tenantID, logicalOperationID, string(kind)))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrReservationNotFound
		}
		if err != nil {
			return err
		}
		switch res.State {
		case "settled":
			return nil // 幂等
		case "released":
			return ErrReservationReleased
		}
		if _, err := tx.Exec(ctx,
			`UPDATE tenant_quotas SET
			   reserved_units=GREATEST(reserved_units-$3, 0),
			   consumed_units=consumed_units+$4,
			   price_version=$5, updated_at=now()
			 WHERE tenant_id=$1 AND usage_kind=$2`,
			tenantID, string(kind), res.ReservedUnits, actualUnits, priceVersion); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO usage_ledger (id, tenant_id, logical_operation_id, usage_kind, quantity, unit, price_version)
			 VALUES (gen_random_uuid(), $1,$2,$3,$4,$5,$6)
			 ON CONFLICT (tenant_id, logical_operation_id, usage_kind) DO NOTHING`,
			tenantID, logicalOperationID, string(kind), actualUnits, string(kind), priceVersion); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE quota_reservations SET state='settled', updated_at=now() WHERE id=$1`, res.ID)
		return err
	})
}

// Release 释放预占。重复释放幂等；已结算返回错误。
func (s *PGStore) Release(ctx context.Context, tenantID, logicalOperationID string, kind Kind) error {
	return tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		res, err := scanReservation(tx.QueryRow(ctx,
			`SELECT `+reservationColumns+` FROM quota_reservations
			 WHERE tenant_id=$1 AND logical_operation_id=$2 AND usage_kind=$3 FOR UPDATE`,
			tenantID, logicalOperationID, string(kind)))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrReservationNotFound
		}
		if err != nil {
			return err
		}
		switch res.State {
		case "released":
			return nil // 幂等
		case "settled":
			return ErrReservationSettled
		}
		if _, err := tx.Exec(ctx,
			`UPDATE tenant_quotas SET reserved_units=GREATEST(reserved_units-$3, 0), updated_at=now()
			 WHERE tenant_id=$1 AND usage_kind=$2`,
			tenantID, string(kind), res.ReservedUnits); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE quota_reservations SET state='released', updated_at=now() WHERE id=$1`, res.ID)
		return err
	})
}

// GetQuota 读取额度；无记录时返回不限量默认值。
func (s *PGStore) GetQuota(ctx context.Context, tenantID string, kind Kind) (*Quota, error) {
	q := &Quota{TenantID: tenantID, Kind: kind, LimitUnits: -1}
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var kindStr string
		serr := tx.QueryRow(ctx,
			`SELECT usage_kind, limit_units, reserved_units, consumed_units, price_version, updated_at
			 FROM tenant_quotas WHERE tenant_id=$1 AND usage_kind=$2`,
			tenantID, string(kind)).Scan(&kindStr, &q.LimitUnits, &q.ReservedUnits, &q.ConsumedUnits, &q.PriceVersion, &q.UpdatedAt)
		if errors.Is(serr, pgx.ErrNoRows) {
			return nil
		}
		return serr
	})
	if err != nil {
		return nil, err
	}
	return q, nil
}

// SetLimit 配置/更新额度上限与定价版本。
func (s *PGStore) SetLimit(ctx context.Context, tenantID string, kind Kind, limitUnits float64, priceVersion string) error {
	return tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO tenant_quotas (tenant_id, usage_kind, limit_units, price_version)
			 VALUES ($1,$2,$3,$4)
			 ON CONFLICT (tenant_id, usage_kind) DO UPDATE
			   SET limit_units=EXCLUDED.limit_units, price_version=EXCLUDED.price_version, updated_at=now()`,
			tenantID, string(kind), limitUnits, priceVersion)
		return err
	})
}

// UsageSummary 返回指定月份（YYYY-MM，空=当月 UTC）的用户计费用量汇总。
// 当前仅计量生成时长；成本字段待正式 TTS 定价表接入。
func (s *PGStore) UsageSummary(ctx context.Context, tenantID, month string) (seconds float64, costUnits float64, err error) {
	start, end, err := monthRange(month)
	if err != nil {
		return 0, 0, err
	}
	err = tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT COALESCE(SUM(quantity),0) FROM usage_ledger
			 WHERE tenant_id=$1 AND usage_kind=$2 AND created_at >= $3 AND created_at < $4`,
			tenantID, string(KindGenSeconds), start, end).Scan(&seconds)
	})
	if err != nil {
		return 0, 0, err
	}
	return seconds, 0, nil
}

func monthRange(month string) (time.Time, time.Time, error) {
	if month == "" {
		month = time.Now().UTC().Format("2006-01")
	}
	start, err := time.Parse("2006-01", month)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return start, start.AddDate(0, 1, 0), nil
}
