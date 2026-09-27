//go:build pg

package tenant

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const lifecycleTenant = "00000000-0000-0000-0000-0000000000e9"

func TestTenantLifecycleStatus(t *testing.T) {
	dsn := os.Getenv("PPTS_TEST_DATABASE")
	if dsn == "" {
		t.Skip("PPTS_TEST_DATABASE not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenants(id,name,status,suspended_at) VALUES ($1,'lifecycle','active',NULL)
		 ON CONFLICT (id) DO UPDATE SET status='active', suspended_at=NULL, updated_at=now()`, lifecycleTenant); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}

	store := NewPGStore(pool)
	active, err := store.TenantActive(ctx, lifecycleTenant)
	if err != nil || !active {
		t.Fatalf("TenantActive active = %v err=%v", active, err)
	}
	if err := store.Suspend(ctx, lifecycleTenant); err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	status, err := store.Status(ctx, lifecycleTenant)
	if err != nil || status != StatusSuspended {
		t.Fatalf("Status suspended = %s err=%v", status, err)
	}
	active, err = store.TenantActive(ctx, lifecycleTenant)
	if err != nil || active {
		t.Fatalf("TenantActive suspended = %v err=%v", active, err)
	}
	if err := store.Resume(ctx, lifecycleTenant); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	status, err = store.Status(ctx, lifecycleTenant)
	if err != nil || status != StatusActive {
		t.Fatalf("Status active = %s err=%v", status, err)
	}
	if _, err := store.Status(ctx, "00000000-0000-0000-0000-0000000000ee"); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("Status missing = %v want ErrTenantNotFound", err)
	}
}

// TestTenantListTenantsPageKeyset 游标分页稳定翻页：同刻创建的租户不漏、不重、跨页连续。
func TestTenantListTenantsPageKeyset(t *testing.T) {
	dsn := os.Getenv("PPTS_TEST_DATABASE")
	if dsn == "" {
		t.Skip("PPTS_TEST_DATABASE not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	store := NewPGStore(pool)

	// 播种 5 个同刻租户（同 created_at），验证 keyset(id) 兜底不漏页。
	base := "20000000-0000-0000-0000-00000000000" // 36 位：末段 11 个 0 + 追加 1 位
	for i := 0; i < 5; i++ {
		id := base + string(rune('0'+i))
		if _, err := pool.Exec(ctx,
			`INSERT INTO tenants(id,name,status) VALUES ($1,$2,'active') ON CONFLICT (id) DO NOTHING`,
			id, "page-"+id); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	var all []string
	seen := map[string]bool{}
	cursor := ""
	for {
		rows, next, err := store.ListTenantsPage(ctx, cursor, 2)
		if err != nil {
			t.Fatalf("ListTenantsPage: %v", err)
		}
		for _, r := range rows {
			if seen[r.ID] {
				t.Fatalf("duplicate tenant %q across pages", r.ID)
			}
			seen[r.ID] = true
			all = append(all, r.ID)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	for i := 0; i < 5; i++ {
		id := base + string(rune('0'+i))
		if !seen[id] {
			t.Fatalf("seeded tenant %q missing from paged result", id)
		}
	}
	if len(all) != len(seen) {
		t.Fatalf("paged rows=%d seen=%d", len(all), len(seen))
	}
}
