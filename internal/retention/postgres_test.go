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
		"TRUNCATE source_revisions, uploads, jobs, job_steps, projects, tenants RESTART IDENTITY CASCADE"); err != nil {
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
