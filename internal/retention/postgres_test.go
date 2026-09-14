//go:build pg

package retention

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/tenant"
)

const (
	rTenant   = "00000000-0000-0000-0000-0000000000f1"
	rProjectA = "00000000-0000-0000-0000-0000000000f2" // 保留期
	rProjectB = "00000000-0000-0000-0000-0000000000f3" // 处理后删除
	rRevA     = "00000000-0000-0000-0000-0000000000f4"
	rRevB     = "00000000-0000-0000-0000-0000000000f5"
	rUploadP  = "00000000-0000-0000-0000-0000000000f6" // pending 孤儿
	rUploadC  = "00000000-0000-0000-0000-0000000000f7" // completed + delete_source_after
	rJobB     = "00000000-0000-0000-0000-0000000000f8"
	rResOld   = "00000000-0000-0000-0000-0000000000f9"
	rResFresh = "00000000-0000-0000-0000-0000000000fa"
)

func retentionKey(id string) objectstore.ObjectKey {
	return objectstore.ObjectKey{TenantID: rTenant, ProjectID: rProjectA, Revision: "src", AssetType: "source", AssetID: id, Ext: "pptx"}
}

func setupRetention(t *testing.T) (*PGStore, objectstore.ObjectStore) {
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
	if _, err := pool.Exec(ctx,
		"TRUNCATE quota_reservations, tenant_quotas, source_revisions, uploads, jobs, job_steps, projects, tenants CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO tenants(id,name) VALUES ($1,'retention')", rTenant); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	objects := objectstore.NewLocal(t.TempDir(), []byte("retention-secret"))
	return NewPGStore(pool), objects
}

func putRetentionObject(t *testing.T, objects objectstore.ObjectStore, key objectstore.ObjectKey) {
	t.Helper()
	if err := objects.Put(context.Background(), key, bytes.NewReader([]byte("pptx")), objectstore.ObjectMeta{Size: 4}); err != nil {
		t.Fatalf("put %s: %v", key, err)
	}
}

func TestSweeperDeletesExpiredAndOrphanObjects(t *testing.T) {
	store, objects := setupRetention(t)
	ctx := context.Background()
	old := time.Now().Add(-48 * time.Hour)

	keyA := retentionKey("aaaa")
	keyB := retentionKey("bbbb")
	keyP := retentionKey("orphan")
	putRetentionObject(t, objects, keyA)
	putRetentionObject(t, objects, keyB)
	putRetentionObject(t, objects, keyP)

	if err := tenant.Run(ctx, store.pool, rTenant, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO projects(id,tenant_id,owner_user,title,source_retention_days) VALUES ($1,$2,'tester','retA',1)`,
			rProjectA, rTenant); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO projects(id,tenant_id,owner_user,title,delete_source_after) VALUES ($1,$2,'tester','retB',true)`,
			rProjectB, rTenant); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO source_revisions(id,project_id,tenant_id,revision_no,source_hash,object_key,parser_version,created_at)
			 VALUES ($1,$2,$3,1,'hashA',$4,'v1',$5)`,
			rRevA, rProjectA, rTenant, keyA.String(), old); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO source_revisions(id,project_id,tenant_id,revision_no,source_hash,object_key,parser_version,created_at)
			 VALUES ($1,$2,$3,1,'hashB',$4,'v1',now())`,
			rRevB, rProjectB, rTenant, keyB.String()); err != nil {
			return err
		}
		// 解析成功 + 会话选择"处理后删除"。
		if _, err := tx.Exec(ctx,
			`INSERT INTO jobs(id,tenant_id,project_id,kind,state) VALUES ($1,$2,$3,'parse','succeeded')`, rJobB, rTenant, rProjectB); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO uploads(id,tenant_id,project_id,filename,size_bytes,delete_source_after,state,object_key,source_revision_id,job_id,created_at)
			 VALUES ($1,$2,$3,'b.pptx',4,true,'completed',$4,$5,$6,now())`,
			rUploadC, rTenant, rProjectB, keyB.String(), rRevB, rJobB); err != nil {
			return err
		}
		// 超时仍 pending 的孤儿上传。
		if _, err := tx.Exec(ctx,
			`INSERT INTO uploads(id,tenant_id,project_id,filename,size_bytes,state,object_key,created_at)
			 VALUES ($1,$2,$3,'p.pptx',4,'pending',$4,$5)`,
			rUploadP, rTenant, rProjectA, keyP.String(), old); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	sweeper := NewSweeper(store, objects, 24*time.Hour, nil)
	if err := sweeper.Sweep(ctx); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	// 三个对象都应被删除。
	for _, key := range []objectstore.ObjectKey{keyA, keyB, keyP} {
		if _, _, err := objects.Get(ctx, key); !errors.Is(err, objectstore.ErrObjectNotFound) {
			t.Fatalf("object %s should be deleted, got %v", key.String(), err)
		}
	}
	// 源版本标记已删除；孤儿会话被中止。
	if err := tenant.Run(ctx, store.pool, rTenant, func(ctx context.Context, tx pgx.Tx) error {
		var deletedA, deletedB *time.Time
		if err := tx.QueryRow(ctx, `SELECT source_deleted_at FROM source_revisions WHERE id=$1`, rRevA).Scan(&deletedA); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT source_deleted_at FROM source_revisions WHERE id=$1`, rRevB).Scan(&deletedB); err != nil {
			return err
		}
		if deletedA == nil || deletedB == nil {
			t.Fatalf("source_deleted_at not set: A=%v B=%v", deletedA, deletedB)
		}
		var state string
		if err := tx.QueryRow(ctx, `SELECT state FROM uploads WHERE id=$1`, rUploadP).Scan(&state); err != nil {
			return err
		}
		if state != "aborted" {
			t.Fatalf("orphan upload state = %s want aborted", state)
		}
		return nil
	}); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

// 未到期的源对象不应被删除。
func TestSweeperKeepsFreshSource(t *testing.T) {
	store, objects := setupRetention(t)
	ctx := context.Background()
	key := retentionKey("fresh")
	putRetentionObject(t, objects, key)

	if err := tenant.Run(ctx, store.pool, rTenant, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO projects(id,tenant_id,owner_user,title,source_retention_days) VALUES ($1,$2,'tester','retA',30)`,
			rProjectA, rTenant); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO source_revisions(id,project_id,tenant_id,revision_no,source_hash,object_key,parser_version,created_at)
			 VALUES ($1,$2,$3,1,'hashF',$4,'v1',now())`, rRevA, rProjectA, rTenant, key.String())
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := NewSweeper(store, objects, 24*time.Hour, nil).Sweep(ctx); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if _, _, err := objects.Get(ctx, key); err != nil {
		t.Fatalf("fresh source should remain, got %v", err)
	}
}

func TestSweeperReleasesStaleQuotaReservations(t *testing.T) {
	store, objects := setupRetention(t)
	ctx := context.Background()
	old := time.Now().Add(-48 * time.Hour)

	if err := tenant.Run(ctx, store.pool, rTenant, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO tenant_quotas(tenant_id,usage_kind,limit_units,reserved_units)
			 VALUES ($1,'gen_seconds',100,30)`, rTenant); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO quota_reservations(id,tenant_id,logical_operation_id,usage_kind,reserved_units,state,created_at)
			 VALUES ($1,$2,'old-op','gen_seconds',10,'reserved',$3)`, rResOld, rTenant, old); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO quota_reservations(id,tenant_id,logical_operation_id,usage_kind,reserved_units,state,created_at)
			 VALUES ($1,$2,'fresh-op','gen_seconds',20,'reserved',now())`, rResFresh, rTenant)
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := NewSweeper(store, objects, 24*time.Hour, nil).WithQuotaReservationTTL(24 * time.Hour).Sweep(ctx); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	if err := tenant.Run(ctx, store.pool, rTenant, func(ctx context.Context, tx pgx.Tx) error {
		var reserved float64
		if err := tx.QueryRow(ctx, `SELECT reserved_units FROM tenant_quotas WHERE tenant_id=$1 AND usage_kind='gen_seconds'`, rTenant).Scan(&reserved); err != nil {
			return err
		}
		if reserved != 20 {
			t.Fatalf("reserved_units = %v want 20", reserved)
		}
		states := map[string]string{}
		rows, err := tx.Query(ctx, `SELECT logical_operation_id, state FROM quota_reservations WHERE tenant_id=$1`, rTenant)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var op, state string
			if err := rows.Scan(&op, &state); err != nil {
				return err
			}
			states[op] = state
		}
		if states["old-op"] != "released" || states["fresh-op"] != "reserved" {
			t.Fatalf("states = %+v", states)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

// 项目未设保留期时，回退到租户级默认源保留期。
func TestSweeperUsesTenantDefaultSourceRetention(t *testing.T) {
	store, objects := setupRetention(t)
	ctx := context.Background()
	old := time.Now().Add(-48 * time.Hour)
	key := retentionKey("tenant-default")
	putRetentionObject(t, objects, key)

	if err := tenant.Run(ctx, store.pool, rTenant, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`UPDATE tenants SET policy='{"source_retention_days":1}'::jsonb WHERE id=$1`, rTenant); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO projects(id,tenant_id,owner_user,title) VALUES ($1,$2,'tester','retA')`,
			rProjectA, rTenant); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO source_revisions(id,project_id,tenant_id,revision_no,source_hash,object_key,parser_version,created_at)
			 VALUES ($1,$2,$3,1,'hashTD',$4,'v1',$5)`, rRevA, rProjectA, rTenant, key.String(), old)
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := NewSweeper(store, objects, 24*time.Hour, nil).Sweep(ctx); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if _, _, err := objects.Get(ctx, key); !errors.Is(err, objectstore.ErrObjectNotFound) {
		t.Fatalf("tenant-default-expired source should be deleted, got %v", err)
	}
}

// 派生产物按租户分档保留期过期后删除对象、清单与 artifacts 行。
func TestSweeperDeletesExpiredDerivedTiers(t *testing.T) {
	store, objects := setupRetention(t)
	ctx := context.Background()
	old := time.Now().Add(-48 * time.Hour)
	oldKey := objectstore.ObjectKey{TenantID: rTenant, ProjectID: rProjectA, Revision: "artifact", AssetType: "artifact", AssetID: "hashA", Ext: "srt"}
	freshKey := objectstore.ObjectKey{TenantID: rTenant, ProjectID: rProjectA, Revision: "artifact", AssetType: "artifact", AssetID: "hashB", Ext: "srt"}
	putRetentionObject(t, objects, oldKey)
	putRetentionObject(t, objects, freshKey)

	if err := tenant.Run(ctx, store.pool, rTenant, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`UPDATE tenants SET policy='{"artifact_retention_days":1}'::jsonb WHERE id=$1`, rTenant); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO projects(id,tenant_id,owner_user,title) VALUES ($1,$2,'tester','retA')`,
			rProjectA, rTenant); err != nil {
			return err
		}
		for _, in := range []struct {
			key        objectstore.ObjectKey
			artifactID string
			snapshot   string
			created    time.Time
		}{
			{oldKey, "00000000-0000-0000-0000-0000000000fb", "snap-a", old},
			{freshKey, "00000000-0000-0000-0000-0000000000fc", "snap-b", time.Now()},
		} {
			if _, err := tx.Exec(ctx,
				`INSERT INTO object_inventory(object_key,tenant_id,project_id,revision,asset_type,asset_id,ext,size_bytes,updated_at)
				 VALUES ($1,$2,$3,'artifact','artifact',$4,'srt',10,$5)`,
				in.key.String(), rTenant, rProjectA, in.key.AssetID, in.created); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO artifacts(id,tenant_id,project_id,snapshot_hash,format,object_key,content_hash,size_bytes,created_at)
				 VALUES ($1,$2,$3,$4,'srt',$5,$6,10,$7)`,
				in.artifactID, rTenant, rProjectA, in.snapshot, in.key.String(), in.key.AssetID, in.created); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := NewSweeper(store, objects, 24*time.Hour, nil).Sweep(ctx); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if _, _, err := objects.Get(ctx, oldKey); !errors.Is(err, objectstore.ErrObjectNotFound) {
		t.Fatalf("expired artifact object should be deleted, got %v", err)
	}
	if _, _, err := objects.Get(ctx, freshKey); err != nil {
		t.Fatalf("fresh artifact object should remain, got %v", err)
	}
	if err := tenant.Run(ctx, store.pool, rTenant, func(ctx context.Context, tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM object_inventory WHERE tenant_id=$1`, rTenant).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Fatalf("object_inventory rows = %d want 1", count)
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM artifacts WHERE tenant_id=$1`, rTenant).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Fatalf("artifacts rows = %d want 1", count)
		}
		return nil
	}); err != nil {
		t.Fatalf("verify: %v", err)
	}
}
