//go:build pg

package artifact

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const artifactTenant = "00000000-0000-0000-0000-0000000000bb"

func setupArtifactStore(t *testing.T) *PGStore {
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
	if _, err := pool.Exec(context.Background(), "TRUNCATE artifacts CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return NewPGStore(pool)
}

func TestPGStoreCreateIsIdempotentBySnapshotAndFormat(t *testing.T) {
	store := setupArtifactStore(t)
	ctx := context.Background()
	in := NewArtifact{
		ProjectID: "00000000-0000-0000-0000-0000000000cc", SnapshotHash: "snap-1",
		Format: FormatMP4, ObjectKey: "tenant/project/artifact/artifact/hash.mp4",
		ContentHash: "hash-1", SizeBytes: 123,
	}
	first, err := store.Create(ctx, artifactTenant, in)
	if err != nil {
		t.Fatalf("Create first: %v", err)
	}
	second, err := store.Create(ctx, artifactTenant, in)
	if err != nil {
		t.Fatalf("Create second: %v", err)
	}
	if first.ID != second.ID || second.ObjectKey != in.ObjectKey {
		t.Fatalf("idempotent mismatch: first=%+v second=%+v", first, second)
	}
	got, err := store.Get(ctx, artifactTenant, first.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Format != FormatMP4 || got.SizeBytes != 123 || got.SnapshotHash != "snap-1" {
		t.Fatalf("got = %+v", got)
	}
	if _, err := store.Get(ctx, "00000000-0000-0000-0000-0000000000aa", first.ID); err != ErrNotFound {
		t.Fatalf("cross-tenant get err = %v", err)
	}
}
