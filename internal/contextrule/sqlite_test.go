package contextrule

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/migrations"
)

func newSQLiteContextRuleStore(t *testing.T) *SQLiteStore {
	t.Helper()
	ctx := context.Background()
	sqldb, err := db.OpenSQLite(ctx, filepath.Join(t.TempDir(), "ppts.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	fsys, _ := migrations.SQLite()
	if _, err := db.MigrateSQLite(ctx, sqldb, fsys); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.EnsureLocalIdentity(ctx, sqldb); err != nil {
		t.Fatalf("identity: %v", err)
	}
	return NewSQLiteStore(sqldb)
}

// TestSQLiteContextRuleRoundTrip 单行=单规则模型：CRUD + 精确字段保真。
func TestSQLiteContextRuleRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteContextRuleStore(t)
	const tenant = db.LocalTenantID

	rec := &Record{
		ID:          "r-1",
		TenantID:    tenant,
		Pattern:     `([0-9]+)(%)`,
		Replacement: "百分之$1",
		Priority:    10,
		Enabled:     true,
	}
	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := store.ListByTenant(ctx, tenant)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("list len = %d want 1", len(got))
	}
	g := got[0]
	if g.Pattern != rec.Pattern || g.Replacement != rec.Replacement || g.Priority != 10 || !g.Enabled {
		t.Fatalf("roundtrip mismatch: %+v", g)
	}
	if g.CreatedAt == 0 {
		t.Fatalf("createdAt not set: %+v", g)
	}

	// Update：改 replacement + disabled。
	g.Replacement = "百分之$1点"
	g.Enabled = false
	if err := store.Update(ctx, g); err != nil {
		t.Fatalf("update: %v", err)
	}
	got2, _ := store.ListByTenant(ctx, tenant)
	if len(got2) != 1 || got2[0].Replacement != "百分之$1点" || got2[0].Enabled {
		t.Fatalf("update mismatch: %+v", got2)
	}

	// Delete + not found。
	if err := store.Delete(ctx, tenant, "r-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := store.Delete(ctx, tenant, "r-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete want ErrNotFound, got %v", err)
	}
}

// TestSQLiteLoadAllEffective 租户行 + 平台 NULL 行按 priority 合并；disabled 被过滤。
func TestSQLiteLoadAllEffective(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteContextRuleStore(t)
	const tenant = db.LocalTenantID

	recs := []*Record{
		{ID: "r-1", TenantID: tenant, Pattern: `a`, Replacement: "A", Priority: 20, Enabled: true},
		{ID: "r-2", Pattern: `b`, Replacement: "B", Priority: 10, Enabled: true}, // 平台行 tenant_id NULL
		{ID: "r-3", TenantID: tenant, Pattern: `c`, Replacement: "C", Priority: 5, Enabled: false},
	}
	for _, r := range recs {
		if err := store.Create(ctx, r); err != nil {
			t.Fatalf("create %s: %v", r.ID, err)
		}
	}
	got, err := store.LoadAllEffective(ctx, tenant)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// priority 升序：r-2(10) → r-1(20)；r-3 disabled 被过滤。
	if len(got) != 2 {
		t.Fatalf("len = %d want 2 (got %+v)", len(got), got)
	}
	if got[0].ID != "r-2" || got[1].ID != "r-1" {
		t.Fatalf("order wrong: %+v", got)
	}
}
