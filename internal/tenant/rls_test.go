//go:build pg

package tenant_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/tenant"
)

const (
	rlsTenantA  = "00000000-0000-0000-0000-0000000000a1"
	rlsTenantB  = "00000000-0000-0000-0000-0000000000b1"
	rlsProjectA = "00000000-0000-0000-0000-0000000000a2"
	rlsProjectB = "00000000-0000-0000-0000-0000000000b2"
)

func rlsPool(t *testing.T) *pgxpool.Pool {
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
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "TRUNCATE projects, tenants RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	for _, id := range []string{rlsTenantA, rlsTenantB} {
		if _, err := pool.Exec(ctx, "INSERT INTO tenants(id,name) VALUES ($1,$2)", id, "rls"); err != nil {
			t.Fatalf("seed tenant: %v", err)
		}
	}
	for _, p := range []struct{ tenantID, projectID string }{
		{rlsTenantA, rlsProjectA}, {rlsTenantB, rlsProjectB},
	} {
		if err := tenant.Run(ctx, pool, p.tenantID, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx,
				"INSERT INTO projects(id,tenant_id,owner_user,title) VALUES ($1,$2,'tester','p')",
				p.projectID, p.tenantID)
			return err
		}); err != nil {
			t.Fatalf("seed project: %v", err)
		}
	}
	return pool
}

func countProjects(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (int, error) {
	var n int
	err := q.QueryRow(ctx, "SELECT count(*) FROM projects").Scan(&n)
	return n, err
}

// 上下文缺失：不可见任何行，且写入被 WITH CHECK 拒绝。
func TestRLSMissingContextDenied(t *testing.T) {
	pool := rlsPool(t)
	ctx := context.Background()

	// 未设置 app.tenant_id 直接查询：RLS 拒绝，可见行数为 0。
	if n, err := countProjects(ctx, pool); err != nil || n != 0 {
		t.Fatalf("missing context select: n=%d err=%v want n=0", n, err)
	}

	// 空租户上下文写入：应被拒绝。
	if err := tenant.Run(ctx, pool, "", func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			"INSERT INTO projects(id,tenant_id,owner_user,title) VALUES ($1,$2,'tester','x')", rlsProjectA, rlsTenantA)
		return err
	}); err == nil {
		t.Fatalf("missing context insert: want error (RLS WITH CHECK), got nil")
	}
}

// 伪造租户写：以 A 的上下文写入 B 的 tenant_id 应被拒绝。
func TestRLSCrossTenantWriteRejected(t *testing.T) {
	pool := rlsPool(t)
	ctx := context.Background()

	err := tenant.Run(ctx, pool, rlsTenantA, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			"INSERT INTO projects(id,tenant_id,owner_user,title) VALUES ($1,$2,'tester','spoof')",
			"00000000-0000-0000-0000-0000000000c3", rlsTenantB)
		return err
	})
	if err == nil {
		t.Fatalf("cross-tenant insert under tenant A: want error, got nil")
	}
}

// 租户隔离：各自只可见本租户项目；单连接连续切租户不串。
func TestRLSTenantIsolationSameConnection(t *testing.T) {
	dsn := os.Getenv("PPTS_TEST_DATABASE")
	if dsn == "" {
		t.Skip("PPTS_TEST_DATABASE not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.MaxConns = 1 // 强制复用同一连接，验证事务局部 set_config 不串租户
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()
	ctx := context.Background()

	// 复用 rlsPool 的种子需要另一连接，这里直接在同一库上重新播种。
	if _, err := pool.Exec(ctx, "TRUNCATE projects, tenants RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	for _, id := range []string{rlsTenantA, rlsTenantB} {
		if _, err := pool.Exec(ctx, "INSERT INTO tenants(id,name) VALUES ($1,$2)", id, "rls"); err != nil {
			t.Fatalf("seed tenant: %v", err)
		}
	}
	for _, p := range []struct{ tenantID, projectID string }{
		{rlsTenantA, rlsProjectA}, {rlsTenantB, rlsProjectB},
	} {
		if err := tenant.Run(ctx, pool, p.tenantID, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx,
				"INSERT INTO projects(id,tenant_id,owner_user,title) VALUES ($1,$2,'tester','p')",
				p.projectID, p.tenantID)
			return err
		}); err != nil {
			t.Fatalf("seed project: %v", err)
		}
	}

	for i := 0; i < 3; i++ {
		var seenA, seenB string
		if err := tenant.Run(ctx, pool, rlsTenantA, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, "SELECT id FROM projects ORDER BY id").Scan(&seenA)
		}); err != nil {
			t.Fatalf("tenant A select: %v", err)
		}
		if err := tenant.Run(ctx, pool, rlsTenantB, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, "SELECT id FROM projects ORDER BY id").Scan(&seenB)
		}); err != nil {
			t.Fatalf("tenant B select: %v", err)
		}
		if seenA != rlsProjectA || seenB != rlsProjectB {
			t.Fatalf("tenant leak on reused connection: A=%s B=%s", seenA, seenB)
		}
	}
}
