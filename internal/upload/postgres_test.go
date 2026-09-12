//go:build pg

package upload

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/tenant"
)

const (
	upTenant  = "00000000-0000-0000-0000-00000000000b"
	upProject = "00000000-0000-0000-0000-0000000000bb"
)

func setupUploadStore(t *testing.T) *PGUploadStore {
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
	if _, err := pool.Exec(context.Background(),
		"TRUNCATE uploads, source_revisions, projects, tenants RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		"INSERT INTO tenants(id,name) VALUES ($1,$2) ON CONFLICT DO NOTHING", upTenant, "upload-test"); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if err := tenant.Run(context.Background(), pool, upTenant, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			"INSERT INTO projects(id,tenant_id,owner_user,title) VALUES ($1,$2,$3,$3) ON CONFLICT DO NOTHING",
			upProject, upTenant, "tester")
		return err
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return NewPGUploadStore(pool)
}

func newUpload() NewUpload {
	return NewUpload{
		ID: "00000000-0000-0000-0000-000000000001", TenantID: upTenant, ProjectID: upProject,
		Filename: "demo.pptx", ContentType: "application/octet-stream",
		ObjectKey: upTenant + "/" + upProject + "/uploads/work/00000000-0000-0000-0000-000000000001.pptx",
		SizeBytes: 8,
	}
}

func TestUploadStoreLifecycle(t *testing.T) {
	s := setupUploadStore(t)
	ctx := context.Background()

	created, err := s.Create(ctx, newUpload())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.State != StatePending || created.ObjectKey == "" {
		t.Fatalf("created = %+v", created)
	}

	got, err := s.Get(ctx, upTenant, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Filename != "demo.pptx" || got.SizeBytes != 8 {
		t.Fatalf("got = %+v", got)
	}

	// 完成 → 幂等重放同一结果。
	done, err := s.Complete(ctx, upTenant, created.ID, "rev-1", "job-1")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if done.State != StateCompleted || done.SourceRevisionID != "rev-1" || done.JobID != "job-1" {
		t.Fatalf("done = %+v", done)
	}
	again, err := s.Complete(ctx, upTenant, created.ID, "rev-1", "job-1")
	if err != nil {
		t.Fatalf("Complete#2: %v", err)
	}
	if again.SourceRevisionID != "rev-1" {
		t.Fatalf("idempotent = %+v", again)
	}

	// 已完成不能再中止。
	if _, err := s.Abort(ctx, upTenant, created.ID); err != ErrAlreadyCompleted {
		t.Fatalf("abort completed: got %v want ErrAlreadyCompleted", err)
	}
}

func TestUploadStoreAbortAndTenantScope(t *testing.T) {
	s := setupUploadStore(t)
	ctx := context.Background()

	created, err := s.Create(ctx, newUpload())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	aborted, err := s.Abort(ctx, upTenant, created.ID)
	if err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if aborted.State != StateAborted {
		t.Fatalf("aborted = %+v", aborted)
	}
	// 已中止不能再完成。
	if _, err := s.Complete(ctx, upTenant, created.ID, "rev", "job"); err != ErrAlreadyAborted {
		t.Fatalf("complete aborted: got %v want ErrAlreadyAborted", err)
	}

	// 跨租户查询拒绝。
	if _, err := s.Get(ctx, "00000000-0000-0000-0000-000000000999", created.ID); err != ErrNotFound {
		t.Fatalf("cross-tenant Get: got %v want ErrNotFound", err)
	}
}
