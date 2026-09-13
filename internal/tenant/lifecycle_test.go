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
