package tenant

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/migrations"
)

func newTenantStore(t *testing.T) *SQLiteStore {
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

func TestSQLiteTenantPolicyStatusStorage(t *testing.T) {
	ctx := context.Background()
	s := newTenantStore(t)
	const tenant = db.LocalTenantID

	// 策略往返
	if err := s.SetPolicy(ctx, tenant, Policy{StorageBackend: "local", SourceRetentionDays: 30}); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	p, err := s.GetPolicy(ctx, tenant)
	if err != nil || p.StorageBackend != "local" || p.SourceRetentionDays != 30 {
		t.Fatalf("get policy: err=%v p=%+v", err, p)
	}
	if backend, _ := s.ObjectStoreBackend(ctx, tenant); backend != "local" {
		t.Fatalf("backend: %s", backend)
	}

	// 状态
	active, err := s.TenantActive(ctx, tenant)
	if err != nil || !active {
		t.Fatalf("should be active: err=%v %v", err, active)
	}
	if err := s.Suspend(ctx, tenant); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if active, _ := s.TenantActive(ctx, tenant); active {
		t.Fatal("should be suspended")
	}
	if err := s.Resume(ctx, tenant); err != nil {
		t.Fatalf("resume: %v", err)
	}

	// 对象清单 + 存储用量
	key := objectstore.ObjectKey{TenantID: tenant, ProjectID: "p1", Revision: "src-01", AssetType: "document", AssetID: "extracted", Ext: "json"}
	if err := s.RecordObject(ctx, key, objectstore.ObjectMeta{Size: 1234, ContentType: "application/json", ContentHash: "h"}); err != nil {
		t.Fatalf("record object: %v", err)
	}
	u, err := s.StorageUsage(ctx, tenant)
	if err != nil || u.OtherBytes != 1234 || u.TotalBytes != 1234 {
		t.Fatalf("storage usage: err=%v u=%+v", err, u)
	}
	if err := s.DeleteObjectRecord(ctx, key); err != nil {
		t.Fatalf("delete object record: %v", err)
	}
	u, _ = s.StorageUsage(ctx, tenant)
	if u.TotalBytes != 0 {
		t.Fatalf("after delete total should be 0, got %d", u.TotalBytes)
	}
}

func TestSQLiteTenantUnsupportedAndPurge(t *testing.T) {
	ctx := context.Background()
	s := newTenantStore(t)
	const tenant = db.LocalTenantID

	if _, err := s.ExportTenant(ctx, tenant, nil); err != ErrNotSupported {
		t.Fatalf("export should be unsupported, got %v", err)
	}
	if _, err := s.GetBYOSCredential(ctx, tenant, "x", nil); err != ErrNotSupported {
		t.Fatalf("byos should be unsupported, got %v", err)
	}

	// Purge：无业务行时返回 0，不报错
	n, err := s.PurgeTenant(ctx, tenant, nil)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	_ = n
}
