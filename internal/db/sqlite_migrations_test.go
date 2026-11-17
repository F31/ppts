package db

import (
	"context"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/F31/ppts/migrations"
)

// nonConstantAddColumnRe 匹配 ALTER TABLE ... ADD COLUMN 中带「括号表达式默认值」的行。
// SQLite 仅在目标表为空时允许该写法；一旦表已有数据（升级路径）会报
// "Cannot add a column with non-constant default"。测试库是空库，MigrateSQLite 用例
// 覆盖不到，故用静态门禁直接拦截这类迁移。
var nonConstantAddColumnRe = regexp.MustCompile(`(?i)ADD\s+COLUMN[^;]*DEFAULT\s*\(`)

// 0022 必须在补唯一索引前合并存量重名：先把项目搬到最早的分组，再删重复行。
// 只跑空库看不到这条路径，必须先用旧 schema 造出重名再应用新迁移。
func TestSQLiteFoldersUniqueMigrationMergesDuplicates(t *testing.T) {
	ctx := context.Background()
	sqldb, err := OpenSQLite(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	fsys, err := migrations.SQLite()
	if err != nil {
		t.Fatalf("sqlite fs: %v", err)
	}
	names, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	sort.Strings(names)
	for _, name := range names {
		if name >= "0022_folders_unique_name.sql" {
			break
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if _, err := sqldb.ExecContext(ctx, string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}

	const tenant = "t1"
	if _, err := sqldb.ExecContext(ctx, `INSERT INTO tenants(id,name) VALUES(?,?)`, tenant, "测试"); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	for _, row := range []struct{ id, created string }{
		{"fold-old", "2026-01-01T00:00:00Z"},
		{"fold-new", "2026-02-01T00:00:00Z"},
	} {
		if _, err := sqldb.ExecContext(ctx,
			`INSERT INTO folders(id,tenant_id,name,created_at) VALUES(?,?,?,?)`,
			row.id, tenant, "重复分组", row.created); err != nil {
			t.Fatalf("seed folder %s: %v", row.id, err)
		}
	}
	if _, err := sqldb.ExecContext(ctx,
		`INSERT INTO projects(id,tenant_id,owner_user,title,folder_id) VALUES(?,?,?,?,?)`,
		"p1", tenant, "u1", "项目", "fold-new"); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	body, err := fs.ReadFile(fsys, "0022_folders_unique_name.sql")
	if err != nil {
		t.Fatalf("read 0022: %v", err)
	}
	if _, err := sqldb.ExecContext(ctx, string(body)); err != nil {
		t.Fatalf("apply 0022: %v", err)
	}

	var count int
	if err := sqldb.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM folders WHERE tenant_id=? AND name=?`, tenant, "重复分组").Scan(&count); err != nil {
		t.Fatalf("count folders: %v", err)
	}
	if count != 1 {
		t.Fatalf("folders after migration = %d, want 1", count)
	}
	var folderID string
	if err := sqldb.QueryRowContext(ctx, `SELECT folder_id FROM projects WHERE id='p1'`).Scan(&folderID); err != nil {
		t.Fatalf("read project folder: %v", err)
	}
	if folderID != "fold-old" {
		t.Fatalf("project folder = %q, want fold-old（最早的分组保留）", folderID)
	}
	if _, err := sqldb.ExecContext(ctx,
		`INSERT INTO folders(id,tenant_id,name) VALUES(?,?,?)`, "fold-dup", tenant, "重复分组"); err == nil {
		t.Fatal("唯一索引没有拦住重名分组")
	}
}

// TestSQLiteMigrationsAvoidNonConstantAddColumnDefaults 扫描内嵌 SQLite 迁移，
// 确保 ADD COLUMN 不使用非恒定默认值（如 strftime(...)/now()）。
func TestSQLiteMigrationsAvoidNonConstantAddColumnDefaults(t *testing.T) {
	fsys, err := migrations.SQLite()
	if err != nil {
		t.Fatalf("sqlite fs: %v", err)
	}
	names, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, name := range names {
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		// 按语句粗略切分（迁移书写规范：语句以分号结尾）。
		for _, stmt := range strings.Split(string(body), ";") {
			if nonConstantAddColumnRe.MatchString(stmt) {
				t.Errorf("%s: ALTER TABLE ADD COLUMN 不允许非恒定默认值（SQLite 在非空表上会失败）；请改为可空列 + UPDATE 回填：\n%s",
					name, strings.TrimSpace(stmt))
			}
		}
	}
}
