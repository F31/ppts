//go:build pg

package project

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/tenant"
)

// TestPGDeleteSourceRevisionFallback 覆盖：
//   - 删除当前版本 → 回退到最新剩余版本；
//   - 删除唯一版本 → ErrDeleteLastRevision；
//   - 回退后新增版本号仍递增（MAX+1，不复用）。
func TestPGDeleteSourceRevisionFallback(t *testing.T) {
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

	const tenantID, owner = "00000000-0000-0000-0000-0000000000d7", "00000000-0000-0000-0000-0000000000d8"
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM projects WHERE tenant_id=$1`, tenantID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, owner)
		_, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE id=$1`, tenantID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES($1,'rev') ON CONFLICT DO NOTHING`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,email) VALUES($1,$2) ON CONFLICT DO NOTHING`, owner, "rev-user@example.com"); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	store := NewPGProjectStore(pool)
	tctx := tenant.WithContext(ctx, tenantID)
	proj, err := store.CreateProject(tctx, tenantID, owner, "rev-test")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM projects WHERE id=$1`, proj.ID) })

	rev := func(name string) *SourceRevision {
		r, err := store.CreateSourceRevision(tctx, tenantID, NewSourceRevision{
			ProjectID: proj.ID, SourceHash: name, ObjectKey: tenantID + "/" + name, ParserVersion: "v1", Filename: name,
		})
		if err != nil {
			t.Fatalf("CreateSourceRevision(%s): %v", name, err)
		}
		return r
	}
	rev("a.pptx")
	rev2 := rev("b.pptx") // current = 2

	got, err := store.GetProject(tctx, tenantID, owner, proj.ID)
	if err != nil || got.CurrentRevision != 2 {
		t.Fatalf("current after import = %d err=%v", got.CurrentRevision, err)
	}

	// 删除当前版本 v2 → 回退到 v1。
	if err := store.DeleteSourceRevision(tctx, tenantID, proj.ID, rev2.RevisionNo); err != nil {
		t.Fatalf("delete current: %v", err)
	}
	got, _ = store.GetProject(tctx, tenantID, owner, proj.ID)
	if got.CurrentRevision != 1 {
		t.Fatalf("current after delete = %d, want 1", got.CurrentRevision)
	}
	list, err := store.ListSourceRevisions(tctx, tenantID, proj.ID)
	if err != nil || len(list) != 1 || list[0].RevisionNo != 1 {
		t.Fatalf("list after delete = %+v err=%v", list, err)
	}

	// 回退后新增版本号应递增（MAX+1=3），不复用 v2。
	rev3 := rev("c.pptx")
	if rev3.RevisionNo != 3 {
		t.Fatalf("new revision no = %d, want 3", rev3.RevisionNo)
	}

	// 删除唯一剩余版本（当前 v3 之外还有 v1；先删 v1 再删 v3 应为 last）。
	if err := store.DeleteSourceRevision(tctx, tenantID, proj.ID, 1); err != nil {
		t.Fatalf("delete v1: %v", err)
	}
	if err := store.DeleteSourceRevision(tctx, tenantID, proj.ID, 3); !errors.Is(err, ErrDeleteLastRevision) {
		t.Fatalf("delete last = %v, want ErrDeleteLastRevision", err)
	}
}
