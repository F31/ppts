package db

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/F31/ppts/migrations"
)

func TestSQLiteMigrateAndLocalIdentity(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "ppts.db")
	sqldb, err := OpenSQLite(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sqldb.Close()

	fsys, err := migrations.SQLite()
	if err != nil {
		t.Fatalf("sqlite fs: %v", err)
	}
	applied, err := MigrateSQLite(ctx, sqldb, fsys)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(applied) == 0 {
		t.Fatal("expected at least one migration applied")
	}
	// 幂等：再跑一次应无新增。
	again, err := MigrateSQLite(ctx, sqldb, fsys)
	if err != nil {
		t.Fatalf("migrate again: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("migrate not idempotent, re-applied %v", again)
	}

	if err := EnsureLocalIdentity(ctx, sqldb); err != nil {
		t.Fatalf("ensure local identity: %v", err)
	}

	// 关键表应存在且可写读。
	var tenants, users, members int
	if err := sqldb.QueryRowContext(ctx, `SELECT COUNT(*) FROM tenants`).Scan(&tenants); err != nil {
		t.Fatalf("count tenants: %v", err)
	}
	if err := sqldb.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&users); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if err := sqldb.QueryRowContext(ctx, `SELECT COUNT(*) FROM tenant_members`).Scan(&members); err != nil {
		t.Fatalf("count members: %v", err)
	}
	if tenants != 1 || users != 1 || members != 1 {
		t.Fatalf("local identity not seeded: tenants=%d users=%d members=%d", tenants, users, members)
	}

	// 插入一个项目验证外键与时间默认值。
	if _, err := sqldb.ExecContext(ctx,
		`INSERT INTO projects (id, tenant_id, owner_user, title) VALUES (?, ?, ?, ?)`,
		"p1", LocalTenantID, LocalUserID, "测试项目"); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	var created string
	if err := sqldb.QueryRowContext(ctx, `SELECT created_at FROM projects WHERE id='p1'`).Scan(&created); err != nil {
		t.Fatalf("select created_at: %v", err)
	}
	if ParseTime(created).IsZero() {
		t.Fatalf("created_at not parseable: %q", created)
	}
}

func TestFromEnvDefaults(t *testing.T) {
	t.Setenv("PPTS_DB_DRIVER", "")
	t.Setenv("PPTS_DATABASE_URL", "")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("from env: %v", err)
	}
	if cfg.Driver != DriverSQLite {
		t.Fatalf("default driver should be sqlite, got %s", cfg.Driver)
	}
	if cfg.DSN == "" {
		t.Fatal("default sqlite DSN should be non-empty")
	}

	t.Setenv("PPTS_DATABASE_URL", "postgres://u:p@localhost:5432/db")
	cfg, err = FromEnv()
	if err != nil {
		t.Fatalf("from env (pg infer): %v", err)
	}
	if cfg.Driver != DriverPostgres {
		t.Fatalf("postgres DSN should infer postgres driver, got %s", cfg.Driver)
	}

	t.Setenv("PPTS_DB_DRIVER", "bogus")
	if _, err := FromEnv(); err == nil {
		t.Fatal("unknown driver should error")
	}
}
