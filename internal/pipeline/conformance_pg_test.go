//go:build pg

package pipeline

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/tenant"
)

// TestPGPipelineConformance 让同一套任务契约用例跑在 PostgreSQL 上。
//
// 与 project 侧不同的是这里**不能** TRUNCATE：同包的其它 pg 用例各自清库，
// 契约用例只在自己新建的租户/项目里活动，跑完删掉即可。
func TestPGPipelineConformance(t *testing.T) {
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

	runPipelineStoreConformance(t, func(t *testing.T) pipelineConformanceEnv {
		tenantID := uuid.NewString()
		projectID := uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES($1,'pipeline-conformance')`, tenantID); err != nil {
			t.Fatalf("seed tenant: %v", err)
		}
		// projects 是 FORCE RLS 表：插入必须在租户上下文里。
		if err := tenant.Run(ctx, pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx,
				`INSERT INTO projects(id,tenant_id,owner_user,title) VALUES($1,$2,'tester','conformance')`,
				projectID, tenantID)
			return err
		}); err != nil {
			t.Fatalf("seed project: %v", err)
		}
		t.Cleanup(func() {
			if err := tenant.Run(ctx, pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
				// job_events 对 tenants 有外键，先清事件再清任务。
				for _, sql := range []string{
					`DELETE FROM job_events WHERE tenant_id=$1`,
					`DELETE FROM job_steps WHERE tenant_id=$1`,
					`DELETE FROM jobs WHERE tenant_id=$1`,
					`DELETE FROM projects WHERE tenant_id=$1`,
				} {
					if _, err := tx.Exec(ctx, sql, tenantID); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Logf("cleanup tenant %s: %v", tenantID, err)
			}
			if _, err := pool.Exec(ctx, `DELETE FROM tenants WHERE id=$1`, tenantID); err != nil {
				t.Logf("cleanup tenant %s: %v", tenantID, err)
			}
		})
		return pipelineConformanceEnv{Store: &PGStore{pool: pool}, Tenant: tenantID, Project: projectID}
	})
}
