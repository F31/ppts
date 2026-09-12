//go:build pg

package usage

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/tenant"
)

const (
	qTenant = "00000000-0000-0000-0000-0000000000e1"
	otherQT = "00000000-0000-0000-0000-0000000000e2"
)

func qStore(t *testing.T) *PGStore {
	t.Helper()
	dsn := os.Getenv("PPTS_TEST_DATABASE")
	if dsn == "" {
		t.Skip("PPTS_TEST_DATABASE not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(),
		"TRUNCATE quota_reservations, tenant_quotas, usage_ledger, tenants RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	for _, id := range []string{qTenant, otherQT} {
		if _, err := pool.Exec(context.Background(),
			"INSERT INTO tenants(id,name) VALUES ($1,$2) ON CONFLICT DO NOTHING", id, "quota"); err != nil {
			t.Fatalf("seed tenant: %v", err)
		}
	}
	return NewPGStore(pool)
}

func TestReserveSettleRelease(t *testing.T) {
	s := qStore(t)
	ctx := context.Background()

	// 不限量默认：预占成功。
	r1, err := s.Reserve(ctx, qTenant, "op-1", KindGenSeconds, 30)
	if err != nil {
		t.Fatalf("Reserve op-1: %v", err)
	}
	if r1.ReservedUnits != 30 || r1.State != "reserved" {
		t.Fatalf("reservation = %+v", r1)
	}
	// 幂等重放：同一逻辑操作返回同一预占，不重复计入。
	again, err := s.Reserve(ctx, qTenant, "op-1", KindGenSeconds, 30)
	if err != nil {
		t.Fatalf("Reserve op-1 again: %v", err)
	}
	if again.ID != r1.ID {
		t.Fatalf("idempotent reserve changed id: %s vs %s", again.ID, r1.ID)
	}
	q, _ := s.GetQuota(ctx, qTenant, KindGenSeconds)
	if q.ReservedUnits != 30 {
		t.Fatalf("reserved double counted: %v", q.ReservedUnits)
	}

	// 结算：reserved→consumed，写账本。
	if err := s.Settle(ctx, qTenant, "op-1", KindGenSeconds, 25, "price-v1"); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	q, _ = s.GetQuota(ctx, qTenant, KindGenSeconds)
	if q.ReservedUnits != 0 || q.ConsumedUnits != 25 {
		t.Fatalf("after settle quota = %+v", q)
	}
	// 重复结算幂等。
	if err := s.Settle(ctx, qTenant, "op-1", KindGenSeconds, 25, "price-v1"); err != nil {
		t.Fatalf("Settle again: %v", err)
	}
	var ledgerCount int
	if err := tenant.Run(ctx, s.pool, qTenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT count(*) FROM usage_ledger WHERE tenant_id=$1", qTenant).Scan(&ledgerCount)
	}); err != nil {
		t.Fatalf("ledger count: %v", err)
	}
	if ledgerCount != 1 {
		t.Fatalf("ledger rows = %d want 1", ledgerCount)
	}

	// 释放：预占不计入 consumed。
	if _, err := s.Reserve(ctx, qTenant, "op-2", KindGenSeconds, 10); err != nil {
		t.Fatalf("Reserve op-2: %v", err)
	}
	if err := s.Release(ctx, qTenant, "op-2", KindGenSeconds); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := s.Release(ctx, qTenant, "op-2", KindGenSeconds); err != nil {
		t.Fatalf("Release idempotent: %v", err)
	}
	q, _ = s.GetQuota(ctx, qTenant, KindGenSeconds)
	if q.ReservedUnits != 0 || q.ConsumedUnits != 25 {
		t.Fatalf("after release quota = %+v", q)
	}
}

func TestReserveEnforcesLimitAtomic(t *testing.T) {
	s := qStore(t)
	ctx := context.Background()
	if err := s.SetLimit(ctx, qTenant, KindGenSeconds, 100, "price-v1"); err != nil {
		t.Fatalf("SetLimit: %v", err)
	}
	if _, err := s.Reserve(ctx, qTenant, "op-a", KindGenSeconds, 30); err != nil {
		t.Fatalf("Reserve 30: %v", err)
	}
	// 30 + 80 > 100 → 拒绝，且不应留下预占记录。
	if _, err := s.Reserve(ctx, qTenant, "op-b", KindGenSeconds, 80); !errors.Is(err, ErrInsufficientQuota) {
		t.Fatalf("oversize reserve: got %v want ErrInsufficientQuota", err)
	}
	var n int
	if err := tenant.Run(ctx, s.pool, qTenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			"SELECT count(*) FROM quota_reservations WHERE tenant_id=$1 AND logical_operation_id='op-b'", qTenant).Scan(&n)
	}); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("rejected reservation should not persist, rows=%d", n)
	}
	// 结算 op-a 后剩余额度 75，op-b 可预占。
	if err := s.Settle(ctx, qTenant, "op-a", KindGenSeconds, 25, "price-v1"); err != nil {
		t.Fatalf("Settle op-a: %v", err)
	}
	if _, err := s.Reserve(ctx, qTenant, "op-b", KindGenSeconds, 70); err != nil {
		t.Fatalf("Reserve after settle: %v", err)
	}
}

func TestReserveCrossTenantIsolated(t *testing.T) {
	s := qStore(t)
	ctx := context.Background()
	if err := s.SetLimit(ctx, qTenant, KindGenSeconds, 10, "price-v1"); err != nil {
		t.Fatalf("SetLimit: %v", err)
	}
	if _, err := s.Reserve(ctx, qTenant, "op-1", KindGenSeconds, 10); err != nil {
		t.Fatalf("Reserve A: %v", err)
	}
	// 租户 A 额度用满，不影响租户 B（B 不限量）。
	if _, err := s.Reserve(ctx, qTenant, "op-2", KindGenSeconds, 1); !errors.Is(err, ErrInsufficientQuota) {
		t.Fatalf("tenant A should be exhausted: %v", err)
	}
	if _, err := s.Reserve(ctx, otherQT, "op-1", KindGenSeconds, 1000); err != nil {
		t.Fatalf("tenant B reserve: %v", err)
	}
}
