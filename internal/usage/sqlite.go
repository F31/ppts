package usage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"

	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/internal/pricing"
)

// SQLiteStore 是 Store 的 SQLite 实现（单租户精简 profile）。
// 单写者下无需 FOR UPDATE；GREATEST 用 SQLite 的 MAX 标量函数替代。
type SQLiteStore struct {
	db        *sql.DB
	priceBook *pricing.Book
}

// NewSQLiteStore 创建 SQLite 用量存储。
func NewSQLiteStore(sqldb *sql.DB) *SQLiteStore { return &SQLiteStore{db: sqldb} }

var _ Store = (*SQLiteStore)(nil)

// WithPriceBook 设置定价表；未设置时金额记 0。
func (s *SQLiteStore) WithPriceBook(b *pricing.Book) *SQLiteStore {
	s.priceBook = b
	return s
}

func (s *SQLiteStore) userAmount(kind Kind, units float64) float64 {
	if s.priceBook == nil {
		return 0
	}
	return s.priceBook.UserAmount(string(kind), units)
}

func (s *SQLiteStore) supplierCost(kind Kind, units float64) float64 {
	if s.priceBook == nil {
		return 0
	}
	return s.priceBook.SupplierAmount(string(kind), units)
}

func (s *SQLiteStore) currency() string {
	if s.priceBook == nil {
		return ""
	}
	return s.priceBook.Currency
}

// Currency 返回定价表币种。
func (s *SQLiteStore) Currency() string { return s.currency() }

const sqReservationColumns = `id, tenant_id, logical_operation_id, usage_kind, reserved_units, state, created_at, updated_at`

type rowScanner interface{ Scan(dest ...any) error }

func sqScanReservation(row rowScanner) (*Reservation, error) {
	var r Reservation
	var kind, state, created, updated string
	if err := row.Scan(&r.ID, &r.TenantID, &r.LogicalOperationID, &kind, &r.ReservedUnits, &state, &created, &updated); err != nil {
		return nil, err
	}
	r.Kind, r.State = Kind(kind), state
	r.CreatedAt = db.ParseTime(created)
	r.UpdatedAt = db.ParseTime(updated)
	return &r, nil
}

func (s *SQLiteStore) Reserve(ctx context.Context, tenantID, logicalOperationID string, kind Kind, units float64) (*Reservation, error) {
	if units < 0 {
		units = 0
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	now := db.Now()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO tenant_quotas (tenant_id, usage_kind, limit_units, updated_at) VALUES (?, ?, -1, ?)
		 ON CONFLICT(tenant_id, usage_kind) DO NOTHING`,
		tenantID, string(kind), now); err != nil {
		return nil, err
	}
	// 幂等：同一逻辑操作只预占一次。
	res, err := tx.ExecContext(ctx,
		`INSERT INTO quota_reservations (id, tenant_id, logical_operation_id, usage_kind, reserved_units, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(tenant_id, logical_operation_id, usage_kind) DO NOTHING`,
		uuid.New().String(), tenantID, logicalOperationID, string(kind), units, now, now)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// 已存在：返回既有预占（不重复计入）。
		r, err := sqScanReservation(tx.QueryRowContext(ctx,
			`SELECT `+sqReservationColumns+` FROM quota_reservations
			 WHERE tenant_id = ? AND logical_operation_id = ? AND usage_kind = ?`,
			tenantID, logicalOperationID, string(kind)))
		if err != nil {
			return nil, err
		}
		return r, tx.Commit()
	}
	// 原子条件更新：不限量或剩余额度足够才计入。
	upd, err := tx.ExecContext(ctx,
		`UPDATE tenant_quotas SET reserved_units = reserved_units + ?, updated_at = ?
		 WHERE tenant_id = ? AND usage_kind = ?
		   AND (limit_units < 0 OR reserved_units + consumed_units + ? <= limit_units)`,
		units, now, tenantID, string(kind), units)
	if err != nil {
		return nil, err
	}
	if n, _ := upd.RowsAffected(); n != 1 {
		return nil, ErrInsufficientQuota
	}
	r, err := sqScanReservation(tx.QueryRowContext(ctx,
		`SELECT `+sqReservationColumns+` FROM quota_reservations
		 WHERE tenant_id = ? AND logical_operation_id = ? AND usage_kind = ?`,
		tenantID, logicalOperationID, string(kind)))
	if err != nil {
		return nil, err
	}
	r.Created = true
	return r, tx.Commit()
}

func (s *SQLiteStore) Settle(ctx context.Context, tenantID, logicalOperationID string, kind Kind, actualUnits float64, priceVersion string) error {
	if actualUnits < 0 {
		actualUnits = 0
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	r, err := sqScanReservation(tx.QueryRowContext(ctx,
		`SELECT `+sqReservationColumns+` FROM quota_reservations
		 WHERE tenant_id = ? AND logical_operation_id = ? AND usage_kind = ?`,
		tenantID, logicalOperationID, string(kind)))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrReservationNotFound
	}
	if err != nil {
		return err
	}
	switch r.State {
	case "settled":
		return tx.Commit() // 幂等
	case "released":
		return ErrReservationReleased
	}
	now := db.Now()
	if _, err := tx.ExecContext(ctx,
		`UPDATE tenant_quotas SET
		   reserved_units = MAX(reserved_units - ?, 0),
		   consumed_units = consumed_units + ?,
		   price_version = ?, updated_at = ?
		 WHERE tenant_id = ? AND usage_kind = ?`,
		r.ReservedUnits, actualUnits, priceVersion, now, tenantID, string(kind)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO usage_ledger (id, tenant_id, logical_operation_id, usage_kind, quantity, unit, price_version,
		   user_amount, supplier_cost, currency, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(tenant_id, logical_operation_id, usage_kind) DO NOTHING`,
		uuid.New().String(), tenantID, logicalOperationID, string(kind), actualUnits, string(kind), priceVersion,
		s.userAmount(kind, actualUnits), s.supplierCost(kind, actualUnits), s.currency(), now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE quota_reservations SET state = 'settled', updated_at = ? WHERE id = ?`, now, r.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) Release(ctx context.Context, tenantID, logicalOperationID string, kind Kind) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	r, err := sqScanReservation(tx.QueryRowContext(ctx,
		`SELECT `+sqReservationColumns+` FROM quota_reservations
		 WHERE tenant_id = ? AND logical_operation_id = ? AND usage_kind = ?`,
		tenantID, logicalOperationID, string(kind)))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrReservationNotFound
	}
	if err != nil {
		return err
	}
	switch r.State {
	case "released":
		return tx.Commit() // 幂等
	case "settled":
		return ErrReservationSettled
	}
	now := db.Now()
	if _, err := tx.ExecContext(ctx,
		`UPDATE tenant_quotas SET reserved_units = MAX(reserved_units - ?, 0), updated_at = ?
		 WHERE tenant_id = ? AND usage_kind = ?`,
		r.ReservedUnits, now, tenantID, string(kind)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE quota_reservations SET state = 'released', updated_at = ? WHERE id = ?`, now, r.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) GetQuota(ctx context.Context, tenantID string, kind Kind) (*Quota, error) {
	q := &Quota{TenantID: tenantID, Kind: kind, LimitUnits: -1}
	var kindStr, updated string
	err := s.db.QueryRowContext(ctx,
		`SELECT usage_kind, limit_units, reserved_units, consumed_units, price_version, updated_at
		 FROM tenant_quotas WHERE tenant_id = ? AND usage_kind = ?`,
		tenantID, string(kind)).Scan(&kindStr, &q.LimitUnits, &q.ReservedUnits, &q.ConsumedUnits, &q.PriceVersion, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return q, nil
	}
	if err != nil {
		return nil, err
	}
	q.UpdatedAt = db.ParseTime(updated)
	return q, nil
}

func (s *SQLiteStore) SetLimit(ctx context.Context, tenantID string, kind Kind, limitUnits float64, priceVersion string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tenant_quotas (tenant_id, usage_kind, limit_units, price_version, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(tenant_id, usage_kind) DO UPDATE
		   SET limit_units = excluded.limit_units, price_version = excluded.price_version, updated_at = excluded.updated_at`,
		tenantID, string(kind), limitUnits, priceVersion, db.Now())
	return err
}

func (s *SQLiteStore) UsageSummary(ctx context.Context, tenantID, month string) (float64, float64, float64, error) {
	start, end, err := monthRange(month)
	if err != nil {
		return 0, 0, 0, err
	}
	var seconds, userAmount, supplierCost float64
	err = s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(quantity),0), COALESCE(SUM(user_amount),0), COALESCE(SUM(supplier_cost),0)
		 FROM usage_ledger
		 WHERE tenant_id = ? AND usage_kind = ? AND created_at >= ? AND created_at < ?`,
		tenantID, string(KindGenSeconds), db.FormatTime(start), db.FormatTime(end)).
		Scan(&seconds, &userAmount, &supplierCost)
	if err != nil {
		return 0, 0, 0, err
	}
	return seconds, userAmount, supplierCost, nil
}

func (s *SQLiteStore) ProjectUsage(ctx context.Context, tenantID, projectID string) (ProjectUsage, error) {
	var out ProjectUsage
	out.ProjectID = projectID
	out.Currency = s.currency()
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(l.quantity),0), COUNT(DISTINCT j.id),
		        COALESCE(SUM(l.user_amount),0), COALESCE(SUM(l.supplier_cost),0)
		 FROM usage_ledger l
		 JOIN jobs j ON j.tenant_id = l.tenant_id
		   AND j.idempotency_key = l.logical_operation_id
		   AND j.kind = 'narration'
		 WHERE l.tenant_id = ? AND j.project_id = ? AND l.usage_kind = ?`,
		tenantID, projectID, string(KindGenSeconds)).
		Scan(&out.Seconds, &out.JobCount, &out.UserAmount, &out.SupplierCost)
	return out, err
}
