//go:build pg

package tenant

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const archiveListTenant = "00000000-0000-0000-0000-0000000000f5"

func archiveListStore(t *testing.T) *PGStore {
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
	resetTenant(ctx, t, pool, archiveListTenant)
	if _, err := pool.Exec(ctx, `INSERT INTO tenants(id,name,status) VALUES ($1,'archive-list','active')`, archiveListTenant); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	return NewPGStore(pool)
}

func TestListAuditArchivesFiltersByType(t *testing.T) {
	s := archiveListStore(t)
	ctx := context.Background()
	if err := Run(ctx, s.pool, archiveListTenant, func(ctx context.Context, tx pgx.Tx) error {
		// 两个审计归档 + 一个无关 work 对象。
		if _, err := tx.Exec(ctx, `
			INSERT INTO object_inventory(object_key,tenant_id,project_id,revision,asset_type,asset_id,size_bytes,updated_at) VALUES
			($1,$2::uuid,'audit','archive','audit','a',10,now()),
			($3,$4::uuid,'audit','archive','audit','b',20,now()),
			($5,$6::uuid,'p','r','work','w',30,now())`,
			archiveListTenant+"/audit/archive/audit/a.jsonl", archiveListTenant,
			archiveListTenant+"/audit/archive/audit/b.jsonl", archiveListTenant,
			archiveListTenant+"/p/r/work/w.json", archiveListTenant); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("seed inventory: %v", err)
	}

	files, err := s.ListAuditArchives(ctx, archiveListTenant, 50)
	if err != nil {
		t.Fatalf("ListAuditArchives: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %d want 2", len(files))
	}
	total := files[0].SizeBytes + files[1].SizeBytes
	if total != 30 {
		t.Fatalf("sum sizes = %d want 30", total)
	}
}

func TestListAuditArchivesClampsLimit(t *testing.T) {
	s := archiveListStore(t)
	files, err := s.ListAuditArchives(context.Background(), archiveListTenant, 9999)
	if err != nil {
		t.Fatalf("ListAuditArchives: %v", err)
	}
	if files == nil {
		t.Fatalf("files should be empty slice not nil")
	}
}
