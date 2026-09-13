//go:build pg

package tenant

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/integrations/objectstore"
)

const storageUsageTenant = "00000000-0000-0000-0000-0000000000d8"
const storageUsageProject = "00000000-0000-0000-0000-0000000000d9"

func storageUsageStore(t *testing.T) *PGStore {
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
	resetTenant(ctx, t, pool, storageUsageTenant)
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenants(id,name,status) VALUES ($1,'storage-usage','active')`, storageUsageTenant); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if err := Run(ctx, pool, storageUsageTenant, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO projects(id,tenant_id,owner_user,title) VALUES ($1::uuid,$2::uuid,'tester','storage')`,
			storageUsageProject, storageUsageTenant)
		return err
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return NewPGStore(pool)
}

func TestStorageUsageAggregatesKnownObjectBytes(t *testing.T) {
	s := storageUsageStore(t)
	ctx := context.Background()
	if err := Run(ctx, s.pool, storageUsageTenant, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO uploads(id,tenant_id,project_id,filename,size_bytes,state,object_key) VALUES
			('00000000-0000-0000-0000-0000000000d1',$1::uuid,$2::uuid,'a.pptx',10,'completed','a'),
			('00000000-0000-0000-0000-0000000000d2',$1::uuid,$2::uuid,'b.pptx',20,'pending','b'),
			('00000000-0000-0000-0000-0000000000d3',$1::uuid,$2::uuid,'c.pptx',30,'aborted','c')`,
			storageUsageTenant, storageUsageProject)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO artifacts(id,tenant_id,project_id,snapshot_hash,format,object_key,content_hash,size_bytes) VALUES
			('00000000-0000-0000-0000-0000000000d4',$1::uuid,$2::uuid,'snap','srt','artifact','hash',7)`,
			storageUsageTenant, storageUsageProject)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO object_inventory(object_key,tenant_id,project_id,revision,asset_type,asset_id,size_bytes) VALUES
			($1,$2::uuid,$3,'rev','audio','audio-1',5)`,
			storageUsageTenant+"/"+storageUsageProject+"/rev/audio/audio-1.mp3", storageUsageTenant, storageUsageProject)
		return err
	}); err != nil {
		t.Fatalf("seed usage rows: %v", err)
	}

	usage, err := s.StorageUsage(ctx, storageUsageTenant)
	if err != nil {
		t.Fatalf("StorageUsage: %v", err)
	}
	if usage.SourceBytes != 30 || usage.SourceObjects != 2 || usage.ArtifactBytes != 7 || usage.ArtifactObjects != 1 ||
		usage.OtherBytes != 5 || usage.OtherObjects != 1 || usage.TotalBytes != 42 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestObjectInventoryRecordAndDelete(t *testing.T) {
	s := storageUsageStore(t)
	ctx := context.Background()
	key := objectstore.ObjectKey{
		TenantID: storageUsageTenant, ProjectID: storageUsageProject,
		Revision: "rev", AssetType: "work", AssetID: "x", Ext: "json",
	}
	if err := s.RecordObject(ctx, key, objectstore.ObjectMeta{Size: 11, ContentType: "application/json", ContentHash: "h"}); err != nil {
		t.Fatalf("RecordObject: %v", err)
	}
	usage, err := s.StorageUsage(ctx, storageUsageTenant)
	if err != nil {
		t.Fatalf("StorageUsage: %v", err)
	}
	if usage.OtherBytes != 11 || usage.OtherObjects != 1 {
		t.Fatalf("usage after record = %+v", usage)
	}
	if err := s.DeleteObjectRecord(ctx, key); err != nil {
		t.Fatalf("DeleteObjectRecord: %v", err)
	}
	usage, err = s.StorageUsage(ctx, storageUsageTenant)
	if err != nil {
		t.Fatalf("StorageUsage after delete: %v", err)
	}
	if usage.OtherBytes != 0 || usage.OtherObjects != 0 {
		t.Fatalf("usage after delete = %+v", usage)
	}
}

func TestStorageUsageMissingTenant(t *testing.T) {
	s := storageUsageStore(t)
	if _, err := s.StorageUsage(context.Background(), "00000000-0000-0000-0000-0000000000df"); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("StorageUsage missing = %v want ErrTenantNotFound", err)
	}
}
