//go:build pg

package tenant

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/integrations/objectstore"
)

const exportPurgeTenant = "00000000-0000-0000-0000-0000000000e8"

const exportPurgeProject = "00000000-0000-0000-0000-0000000000a8"

func exportStore(t *testing.T) *PGStore {
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
	resetTenant(ctx, t, pool, exportPurgeTenant)
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenants(id,name,status) VALUES ($1,'export-purge','active')`, exportPurgeTenant); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if err := Run(ctx, pool, exportPurgeTenant, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO projects(id,tenant_id,owner_user,title) VALUES ($1::uuid,$2::uuid,'tester','demo')
			 ON CONFLICT DO NOTHING`, exportPurgeProject, exportPurgeTenant)
		return err
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return NewPGStore(pool)
}

// resetTenant 清掉既有租户全部业务行与控制面行，保证跨次运行幂等。
func resetTenant(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenantID string) {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tenants WHERE id=$1)`, tenantID).Scan(&exists); err != nil {
		t.Fatalf("check tenant: %v", err)
	}
	if !exists {
		return
	}
	for _, table := range businessTables {
		if err := Run(ctx, pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx,
				"DELETE FROM "+table+" WHERE tenant_id=$1", tenantID)
			return err
		}); err != nil {
			t.Fatalf("reset %s: %v", table, err)
		}
	}
	if _, err := pool.Exec(ctx, `DELETE FROM tenants WHERE id=$1`, tenantID); err != nil {
		t.Fatalf("reset tenant: %v", err)
	}
}

func seedUpload(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if err := Run(ctx, pool, exportPurgeTenant, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO uploads(id,tenant_id,project_id,filename,size_bytes,state,object_key)
			 VALUES ('00000000-0000-0000-0000-0000000000c9',$1::uuid,$2::uuid,'demo.pptx',3,'pending','tenant/export/pending.pptx')`,
			exportPurgeTenant, exportPurgeProject)
		return err
	}); err != nil {
		t.Fatalf("seed upload: %v", err)
	}
}

func TestExportTenantWritesJSONLManifest(t *testing.T) {
	s := exportStore(t)
	ctx := context.Background()
	seedUpload(ctx, t, s.pool)
	objects := objectstore.NewLocal(t.TempDir(), []byte("download-secret"))

	manifest, err := s.ExportTenant(ctx, exportPurgeTenant, objects)
	if err != nil {
		t.Fatalf("ExportTenant: %v", err)
	}
	if manifest.TenantID != exportPurgeTenant || len(manifest.Files) == 0 {
		t.Fatalf("manifest = %+v", manifest)
	}
	var uploadRows int64
	for _, f := range manifest.Files {
		if f.Table == "uploads" {
			uploadRows = f.Rows
			key, err := objectstore.Parse(f.ObjectKey)
			if err != nil {
				t.Fatalf("parse file key: %v", err)
			}
			rc, _, err := objects.Get(ctx, key)
			if err != nil {
				t.Fatalf("export file not persisted: %v", err)
			}
			rc.Close()
		}
	}
	if uploadRows < 1 {
		t.Fatalf("uploads rows = %d want >= 1", uploadRows)
	}
}

func TestPurgeTenantErasesRowsAndSetsDeleted(t *testing.T) {
	s := exportStore(t)
	ctx := context.Background()
	objects := objectstore.NewLocal(t.TempDir(), []byte("download-secret"))

	objKey := objectstore.ObjectKey{
		TenantID: exportPurgeTenant, ProjectID: exportPurgeProject,
		Revision: "uploads", AssetType: "work", AssetID: "garbage", Ext: "pptx",
	}
	uploadID := "00000000-0000-0000-0000-0000000000c8"
	if err := Run(ctx, s.pool, exportPurgeTenant, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO uploads(id,tenant_id,project_id,filename,size_bytes,state,object_key)
			 VALUES ($1::uuid,$2::uuid,$3::uuid,'demo.pptx',3,'pending',$4)`,
			uploadID, exportPurgeTenant, exportPurgeProject, objKey.String())
		return err
	}); err != nil {
		t.Fatalf("seed upload: %v", err)
	}
	if err := objects.Put(ctx, objKey, bytes.NewReader(make([]byte, 3)), objectstore.ObjectMeta{Size: 3}); err != nil {
		t.Fatalf("place object: %v", err)
	}

	deleted, err := s.PurgeTenant(ctx, exportPurgeTenant, objects)
	if err != nil {
		t.Fatalf("PurgeTenant: %v", err)
	}
	if deleted < 2 { // upload + project
		t.Fatalf("deleted = %d want >= 2", deleted)
	}
	status, err := s.Status(ctx, exportPurgeTenant)
	if err != nil || status != StatusDeleted {
		t.Fatalf("status after purge = %s err=%v want deleted", status, err)
	}
	var remaining int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM uploads WHERE tenant_id=$1`, exportPurgeTenant).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("uploads remaining = %d err=%v want 0", remaining, err)
	}
	rc, _, err := objects.Get(ctx, objKey)
	if err == nil {
		rc.Close()
		t.Fatalf("object should be deleted after purge")
	}
	if !errors.Is(err, objectstore.ErrObjectNotFound) {
		t.Fatalf("object err = %v want ErrObjectNotFound", err)
	}
}

func TestPurgeTenantMissingTenant(t *testing.T) {
	s := exportStore(t)
	objects := objectstore.NewLocal(t.TempDir(), []byte("download-secret"))
	if _, err := s.PurgeTenant(context.Background(), "00000000-0000-0000-0000-0000000000e0", objects); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("purge missing = %v want ErrTenantNotFound", err)
	}
}
