package db

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/F31/ppts/migrations"
)

// nonConstantAddColumnRe 匹配 ALTER TABLE ... ADD COLUMN 中带「括号表达式默认值」的行。
// SQLite 仅在目标表为空时允许该写法；一旦表已有数据（升级路径）会报
// "Cannot add a column with non-constant default"。测试库是空库，MigrateSQLite 用例
// 覆盖不到，故用静态门禁直接拦截这类迁移。
var nonConstantAddColumnRe = regexp.MustCompile(`(?i)ADD\s+COLUMN[^;]*DEFAULT\s*\(`)

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
