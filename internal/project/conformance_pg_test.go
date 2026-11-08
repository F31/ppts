//go:build pg

package project

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/tenant"
)

// TestPGConformance 让同一套契约用例跑在 PostgreSQL 上。
//
// 挂在现有 `-tags=pg` 作业里（CI 的 postgres job 带着 PPTS_TEST_DATABASE 跑全仓）。
// 每个子用例自己开一个租户：用例会归档、删标签、改协作者，共享租户会让并发与
// 顺序互相污染，而租户是这张库上唯一免费的隔离维度。
func TestPGConformance(t *testing.T) {
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

	runProjectStoreConformance(t, func(t *testing.T) conformanceEnv {
		return newPGConformanceEnv(t, ctx, pool)
	})
}

func newPGConformanceEnv(t *testing.T, ctx context.Context, pool *pgxpool.Pool) conformanceEnv {
	t.Helper()
	tenantID := uuid.NewString()
	otherTenant := uuid.NewString()
	owner := uuid.NewString()
	other := uuid.NewString()

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed %q: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO tenants(id,name) VALUES($1,'conformance')`, tenantID)
	mustExec(`INSERT INTO tenants(id,name) VALUES($1,'conformance-other')`, otherTenant)
	mustExec(`INSERT INTO users(id,email) VALUES($1,$2)`, owner, owner+"@example.com")
	mustExec(`INSERT INTO users(id,email) VALUES($1,$2)`, other, other+"@example.com")

	t.Cleanup(func() {
		// projects/tags/folders 都是 FORCE RLS 表：不带 app.tenant_id 的 DELETE
		// 会静默删 0 行，接着 users/tenants 的外键把它变成一条看不懂的报错。
		for _, tid := range []string{tenantID, otherTenant} {
			if err := tenant.Run(ctx, pool, tid, func(ctx context.Context, tx pgx.Tx) error {
				for _, sql := range []string{
					`DELETE FROM projects WHERE tenant_id=$1`,
					`DELETE FROM folders WHERE tenant_id=$1`,
					`DELETE FROM tags WHERE tenant_id=$1`,
				} {
					if _, err := tx.Exec(ctx, sql, tid); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Logf("cleanup tenant %s: %v", tid, err)
			}
		}
		for _, uid := range []string{owner, other} {
			if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, uid); err != nil {
				t.Logf("cleanup user %s: %v", uid, err)
			}
		}
		for _, tid := range []string{tenantID, otherTenant} {
			if _, err := pool.Exec(ctx, `DELETE FROM tenants WHERE id=$1`, tid); err != nil {
				t.Logf("cleanup tenant %s: %v", tid, err)
			}
		}
	})

	return conformanceEnv{
		Store: NewPGProjectStore(pool), Tenant: tenantID, Owner: owner, Other: other,
		OtherTenant: otherTenant,
		NewUser: func(t *testing.T) string {
			t.Helper()
			id := uuid.NewString()
			if _, err := pool.Exec(ctx, `INSERT INTO users(id,email) VALUES($1,$2)`, id, id+"@example.com"); err != nil {
				t.Fatalf("建用户: %v", err)
			}
			t.Cleanup(func() {
				if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, id); err != nil {
					t.Logf("cleanup user %s: %v", id, err)
				}
			})
			return id
		},
	}
}
