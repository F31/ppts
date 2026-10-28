//go:build pg

package animation

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/tenant"
)

const (
	anTenant  = "00000000-0000-0000-0000-0000000000c1"
	anProject = "00000000-0000-0000-0000-0000000000c2"
)

func TestPGStoreSuite(t *testing.T) {
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

	if _, err := pool.Exec(ctx,
		"TRUNCATE animation_revisions, projects, tenants CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := pool.Exec(ctx,
		"INSERT INTO tenants(id,name) VALUES ($1,$2) ON CONFLICT DO NOTHING", anTenant, "animation-test"); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if err := tenant.Run(ctx, pool, anTenant, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			"INSERT INTO projects(id,tenant_id,owner_user,title) VALUES ($1,$2,$3,$3)",
			anProject, anTenant, anTenant)
		return err
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	runStoreSuite(t, NewPGStore(pool), anTenant, anProject)
}
