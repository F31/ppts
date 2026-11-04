package narration

import (
	"context"
	"io/fs"
	"path"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/migrations"
)

// 部署前必须跑 `ppts migrate`（0053 / sqlite 0020 才加了 focus 列）。这个用例演练的
// 是那次迁移**在已有数据的库上**会发生什么，而不是在空库上建表：
//
//   - 加列之后旧行还在（不是重建表）；
//   - 旧行的焦点读回来是 NULL / nil，也就是「这段不指任何东西」——而不是空对象
//     或解析失败。旧讲稿不该因为加了这个功能就变得读不出来。
//
// 空库建表那一路所有既有用例都覆盖了，恰恰是「有数据」这一路没人走过。
func TestSQLiteFocusMigrationKeepsExistingRows(t *testing.T) {
	ctx := context.Background()
	full, err := migrations.SQLite()
	if err != nil {
		t.Fatalf("migrations: %v", err)
	}
	const focusMigration = "0020_narration_segment_focus.sql"
	before := withoutMigration(t, full, focusMigration)

	sqldb, err := db.OpenSQLite(ctx, filepath.Join(t.TempDir(), "ppts.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })

	// 1) 先造一个「加列之前」的库，并写进一份老讲稿。
	if _, err := db.MigrateSQLite(ctx, sqldb, before); err != nil {
		t.Fatalf("migrate(before): %v", err)
	}
	if err := db.EnsureLocalIdentity(ctx, sqldb); err != nil {
		t.Fatalf("identity: %v", err)
	}
	const tenant = db.LocalTenantID
	const scriptID = "script-legacy"
	if _, err := sqldb.ExecContext(ctx,
		`INSERT INTO narration_scripts (id, tenant_id, project_id, slide_id, language, source_revision_no)
		 VALUES (?, ?, 'p1', 'slide-1', 'zh-CN', 3)`, scriptID, tenant); err != nil {
		t.Fatalf("insert script: %v", err)
	}
	if _, err := sqldb.ExecContext(ctx,
		`INSERT INTO narration_segments (id, script_id, tenant_id, segment_id, display_text, spoken_text)
		 VALUES ('seg-row-1', ?, ?, 'seg-01', '第一段', '第一段')`, scriptID, tenant); err != nil {
		t.Fatalf("insert segment: %v", err)
	}

	// 2) 应用全量迁移：只该补上焦点这一列。
	applied, err := db.MigrateSQLite(ctx, sqldb, full)
	if err != nil {
		t.Fatalf("migrate(full): %v", err)
	}
	if len(applied) != 1 || applied[0] != focusMigration {
		t.Fatalf("这次只该应用 %s，实得 %v", focusMigration, applied)
	}

	// 3) 旧行还在，焦点是 NULL（不是 '' 也不是 '{}'）。
	var rows int
	if err := sqldb.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM narration_segments WHERE script_id = ?`, scriptID).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Fatalf("迁移后旧分段应仍是 1 行，实得 %d", rows)
	}
	var focusIsNull bool
	if err := sqldb.QueryRowContext(ctx,
		`SELECT focus IS NULL FROM narration_segments WHERE id = 'seg-row-1'`).Scan(&focusIsNull); err != nil {
		t.Fatalf("read focus: %v", err)
	}
	if !focusIsNull {
		t.Fatalf("旧分段的 focus 应为 NULL（= 不指），实得非 NULL")
	}

	// 4) 走产品自己的读路径：老讲稿要能读出来，且 Focus 是 nil。
	s := NewSQLiteStore(sqldb)
	rev, err := s.Get(ctx, tenant, "p1", 3, "slide-1", "zh-CN")
	if err != nil {
		t.Fatalf("get legacy script: %v", err)
	}
	if len(rev.Segments) != 1 {
		t.Fatalf("读回 %d 段，应为 1", len(rev.Segments))
	}
	if rev.Segments[0].Focus != nil {
		t.Fatalf("旧讲稿的 Focus 应为 nil，实得 %+v", *rev.Segments[0].Focus)
	}

	// 5) 再跑一次是空操作（迁移幂等），部署重试不会把列再加一遍。
	again, err := db.MigrateSQLite(ctx, sqldb, full)
	if err != nil {
		t.Fatalf("migrate again: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("重复迁移应无动作，实得 %v", again)
	}
}

// withoutMigration 返回去掉某个文件名后的迁移目录（其余文件原样）。
func withoutMigration(t *testing.T, fsys fs.FS, skip string) fs.FS {
	t.Helper()
	out := fstest.MapFS{}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || path.Base(p) == skip {
			return nil
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		out[p] = &fstest.MapFile{Data: b}
		return nil
	})
	if err != nil {
		t.Fatalf("walk migrations: %v", err)
	}
	if len(out) == 0 {
		t.Fatalf("过滤后没有迁移文件，用例本身失效")
	}
	return out
}
