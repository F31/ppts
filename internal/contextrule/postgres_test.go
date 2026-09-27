//go:build pg

package contextrule

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func pgStore(t *testing.T) *PGStore {
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
	if _, err := pool.Exec(context.Background(), "TRUNCATE contextual_rules CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return NewPGStore(pool)
}

// TestPGContextRuleCRUD PG 存储 CRUD + 平台行隔离。
func TestPGContextRuleCRUD(t *testing.T) {
	ctx := context.Background()
	s := pgStore(t)
	const tenant = "00000000-0000-0000-0000-0000000000c1"

	rec := &Record{ID: "00000000-0000-0000-0000-0000000000c2", TenantID: tenant,
		Pattern: `([0-9]+)(%)`, Replacement: `百分之$1`, Priority: 10, Enabled: true}
	if err := s.Create(ctx, rec); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.ListByTenant(ctx, tenant)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Pattern != rec.Pattern || got[0].Replacement != rec.Replacement {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}

	// 平台行（tenant NULL）不可被租户 list 命中（隔离）。
	plat := &Record{ID: "00000000-0000-0000-0000-0000000000c3",
		Pattern: `a`, Replacement: `A`, Priority: 5, Enabled: true}
	if err := s.Create(ctx, plat); err != nil {
		t.Fatalf("create platform: %v", err)
	}
	got2, _ := s.ListByTenant(ctx, tenant)
	if len(got2) != 1 {
		t.Fatalf("tenant list should exclude platform rows, got %d", len(got2))
	}

	// LoadAllEffective 合并租户+平台，priority 升序。
	eff, err := s.LoadAllEffective(ctx, tenant)
	if err != nil {
		t.Fatalf("load effective: %v", err)
	}
	if len(eff) != 2 {
		t.Fatalf("effective len = %d want 2", len(eff))
	}
	if eff[0].ID != plat.ID || eff[1].ID != rec.ID {
		t.Fatalf("effective order wrong: %+v", eff)
	}

	// Update + Delete + not found。
	rec.Priority = 15
	if err := s.Update(ctx, rec); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := s.Delete(ctx, tenant, rec.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.Delete(ctx, tenant, rec.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete want ErrNotFound, got %v", err)
	}
	// 平台行不能经租户 Delete 删除（隔离）。
	if err := s.Delete(ctx, tenant, plat.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant delete of platform row want ErrNotFound, got %v", err)
	}
}
