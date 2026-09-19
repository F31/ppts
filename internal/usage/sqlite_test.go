package usage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/migrations"
)

func newUsageStore(t *testing.T) *SQLiteStore {
	t.Helper()
	ctx := context.Background()
	sqldb, err := db.OpenSQLite(ctx, filepath.Join(t.TempDir(), "ppts.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	fsys, _ := migrations.SQLite()
	if _, err := db.MigrateSQLite(ctx, sqldb, fsys); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.EnsureLocalIdentity(ctx, sqldb); err != nil {
		t.Fatalf("identity: %v", err)
	}
	return NewSQLiteStore(sqldb)
}

func TestSQLiteUsageReserveSettleRelease(t *testing.T) {
	ctx := context.Background()
	s := newUsageStore(t)
	const tenant = db.LocalTenantID
	const kind = KindGenSeconds

	// 默认不限量
	q, err := s.GetQuota(ctx, tenant, kind)
	if err != nil || q.LimitUnits != -1 {
		t.Fatalf("default quota: err=%v q=%+v", err, q)
	}

	// 预占
	r, err := s.Reserve(ctx, tenant, "op-1", kind, 10)
	if err != nil || !r.Created {
		t.Fatalf("reserve: err=%v r=%+v", err, r)
	}
	// 幂等重放
	r2, err := s.Reserve(ctx, tenant, "op-1", kind, 10)
	if err != nil || r2.Created {
		t.Fatalf("reserve idempotent: err=%v created=%v", err, r2.Created)
	}

	// 结算
	if err := s.Settle(ctx, tenant, "op-1", kind, 8, "v1"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	// 重复结算幂等
	if err := s.Settle(ctx, tenant, "op-1", kind, 8, "v1"); err != nil {
		t.Fatalf("settle idempotent: %v", err)
	}
	q, _ = s.GetQuota(ctx, tenant, kind)
	if q.ConsumedUnits != 8 || q.ReservedUnits != 0 {
		t.Fatalf("after settle: %+v", q)
	}

	// 释放
	if _, err := s.Reserve(ctx, tenant, "op-2", kind, 5); err != nil {
		t.Fatalf("reserve 2: %v", err)
	}
	if err := s.Release(ctx, tenant, "op-2", kind); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := s.Release(ctx, tenant, "op-2", kind); err != nil {
		t.Fatalf("release idempotent: %v", err)
	}
	// 已释放不能再结算
	if err := s.Settle(ctx, tenant, "op-2", kind, 1, "v1"); !errors.Is(err, ErrReservationReleased) {
		t.Fatalf("settle released should error, got %v", err)
	}

	// 上限收紧后超额预占
	if err := s.SetLimit(ctx, tenant, kind, 9, "v1"); err != nil {
		t.Fatalf("set limit: %v", err)
	}
	// 已消耗 8，再预占 2 会超 9
	if _, err := s.Reserve(ctx, tenant, "op-3", kind, 2); !errors.Is(err, ErrInsufficientQuota) {
		t.Fatalf("should be insufficient quota, got %v", err)
	}

	// UsageSummary（当月应含已结算 8 秒）
	sec, _, _, err := s.UsageSummary(ctx, tenant, "")
	if err != nil || sec != 8 {
		t.Fatalf("usage summary: err=%v sec=%v", err, sec)
	}
}
