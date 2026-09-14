//go:build pg

package membership

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	mTenantA = "00000000-0000-0000-0000-0000000000d1"
	mTenantB = "00000000-0000-0000-0000-0000000000d2"
)

func memStore(t *testing.T) *PGStore {
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
		"TRUNCATE tenant_members, tenants CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	for _, id := range []string{mTenantA, mTenantB} {
		if _, err := pool.Exec(context.Background(),
			"INSERT INTO tenants(id,name) VALUES ($1,'mem')", id); err != nil {
			t.Fatalf("seed tenant: %v", err)
		}
	}
	return NewPGStore(pool)
}

func TestMembershipCRUDAndIsolation(t *testing.T) {
	s := memStore(t)
	ctx := context.Background()

	if _, err := s.GetRole(ctx, mTenantA, "user-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRole missing = %v want ErrNotFound", err)
	}
	if err := s.SetRole(ctx, mTenantA, "user-1", RoleOwner); err != nil {
		t.Fatalf("SetRole owner: %v", err)
	}
	if err := s.SetRole(ctx, mTenantA, "user-1", RoleAdmin); err != nil {
		t.Fatalf("SetRole update: %v", err)
	}
	if err := s.SetRole(ctx, mTenantA, "user-2", RoleEditor); err != nil {
		t.Fatalf("SetRole editor: %v", err)
	}
	// 更新应只改角色不改行数。
	members, err := s.List(ctx, mTenantA)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("members = %d want 2", len(members))
	}
	role, err := s.GetRole(ctx, mTenantA, "user-1")
	if err != nil || role != RoleAdmin {
		t.Fatalf("role = %v err=%v want admin", role, err)
	}

	// 跨租户隔离。
	if _, err := s.GetRole(ctx, mTenantB, "user-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant B should not see A members: %v", err)
	}
	membersB, _ := s.List(ctx, mTenantB)
	if len(membersB) != 0 {
		t.Fatalf("tenant B members = %+v", membersB)
	}

	// 移除。
	if err := s.Remove(ctx, mTenantA, "user-2"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := s.Remove(ctx, mTenantA, "user-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Remove missing = %v want ErrNotFound", err)
	}
}

func TestMembershipValidation(t *testing.T) {
	s := memStore(t)
	ctx := context.Background()
	if err := s.SetRole(ctx, "", "user-1", RoleViewer); err == nil {
		t.Fatalf("empty tenant should error")
	}
	if err := s.SetRole(ctx, mTenantA, "", RoleViewer); err == nil {
		t.Fatalf("empty user should error")
	}
	if err := s.SetRole(ctx, mTenantA, "user-1", Role("hacker")); err == nil {
		t.Fatalf("invalid role should error")
	}
}
