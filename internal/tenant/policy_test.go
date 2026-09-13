//go:build pg

package tenant

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestListLifecyclePoliciesReturnsActiveTenants(t *testing.T) {
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
	ids := []string{
		"00000000-0000-0000-0000-0000000000f1",
		"00000000-0000-0000-0000-0000000000f2",
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenants(id,name,status,policy) VALUES
		 ($1::uuid,'active','active','{"storage_transition_days":30,"storage_expiration_days":365}'::jsonb),
		 ($2::uuid,'suspended','suspended','{"storage_transition_days":7}'::jsonb)
		 ON CONFLICT (id) DO UPDATE SET
		   status=EXCLUDED.status,
		   policy=EXCLUDED.policy,
		   updated_at=now()`, ids[0], ids[1]); err != nil {
		t.Fatalf("seed tenants: %v", err)
	}

	settings, err := NewPGStore(pool).ListLifecyclePolicies(ctx)
	if err != nil {
		t.Fatalf("ListLifecyclePolicies: %v", err)
	}
	found := false
	for _, setting := range settings {
		if setting.TenantID == ids[1] {
			t.Fatalf("suspended tenant returned: %+v", setting)
		}
		if setting.TenantID == ids[0] {
			found = true
			if setting.Policy.StorageTransitionDays != 30 || setting.Policy.StorageExpirationDays != 365 {
				t.Fatalf("policy = %+v", setting.Policy)
			}
		}
	}
	if !found {
		t.Fatalf("active tenant %s not returned", ids[0])
	}
}
