//go:build pg

package pronunciation

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/tenant"
)

const dictTenant = "00000000-0000-0000-0000-0000000000c1"

func dictStore(t *testing.T) Store {
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
		"TRUNCATE pronunciation_dictionaries, narration_segments, narration_scripts, jobs, job_steps, source_revisions, projects, tenants CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		"INSERT INTO tenants(id,name) VALUES ($1,$2) ON CONFLICT DO NOTHING", dictTenant, "dict"); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if err := tenant.Run(context.Background(), pool, dictTenant, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			"INSERT INTO projects(id,tenant_id,owner_user,title) VALUES ($1,$2,$3,$3) ON CONFLICT DO NOTHING",
			"00000000-0000-0000-0000-0000000000c2", dictTenant, "dict-owner")
		return err
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return NewPGStore(pool)
}

func TestDictCRUD(t *testing.T) {
	s := dictStore(t)
	ctx := context.Background()

	// Empty list
	dicts, err := s.ListByTenant(ctx, dictTenant)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(dicts) != 0 {
		t.Fatalf("expected empty list, got %d", len(dicts))
	}

	// Create
	rules := Rules{
		{Pattern: "CUDA", Replacement: "C U D A", Enabled: true},
		{Pattern: "MySQL", Replacement: "My Sequel", Enabled: true},
	}
	dict := &Dictionary{
		ID:       "11111111-1111-1111-1111-111111111111",
		TenantID: dictTenant,
		Name:     "tech",
		Rules:    rules,
	}
	if err := s.Create(ctx, dict); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Get
	got, err := s.GetByID(ctx, dictTenant, dict.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "tech" {
		t.Errorf("name = %q, want %q", got.Name, "tech")
	}
	if len(got.Rules) != 2 {
		t.Errorf("rules len = %d, want 2", len(got.Rules))
	}

	// Update
	got.Name = "tech-v2"
	got.Rules = Rules{
		{Pattern: "CUDA", Replacement: "C U D A", Enabled: true},
		{Pattern: "Kubernetes", Replacement: "Koobernetees", Enabled: true},
	}
	if err := s.Update(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	updated, err := s.GetByID(ctx, dictTenant, dict.ID)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if updated.Name != "tech-v2" {
		t.Errorf("updated name = %q, want %q", updated.Name, "tech-v2")
	}
	if len(updated.Rules) != 2 {
		t.Errorf("updated rules len = %d, want 2", len(updated.Rules))
	}

	// List
	dicts, err = s.ListByTenant(ctx, dictTenant)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(dicts) != 1 {
		t.Fatalf("expected 1 dict, got %d", len(dicts))
	}

	// Delete
	if err := s.Delete(ctx, dictTenant, dict.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetByID(ctx, dictTenant, dict.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}

	// Delete non-existent (valid UUID but not in DB)
	if err := s.Delete(ctx, dictTenant, "00000000-0000-0000-0000-000000000099"); err != ErrNotFound {
		t.Errorf("expected ErrNotFound for missing delete, got %v", err)
	}
}

func TestDictGetWrongTenant(t *testing.T) {
	s := dictStore(t)
	ctx := context.Background()

	dict := &Dictionary{
		ID:       "22222222-2222-2222-2222-222222222222",
		TenantID: dictTenant,
		Name:     "isolated",
		Rules:    Rules{{Pattern: "x", Replacement: "y", Enabled: true}},
	}
	if err := s.Create(ctx, dict); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Query with wrong tenant
	_, err := s.GetByID(ctx, "00000000-0000-0000-0000-000000000099", dict.ID)
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound for wrong tenant, got %v", err)
	}
}
