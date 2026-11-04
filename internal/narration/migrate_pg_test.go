//go:build pg

package narration

import (
	"context"
	"io/fs"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/migrations"
)

const pgFocusMigrationFile = "0053_narration_segment_focus.sql"

// TestPGFocusMigrationKeepsExistingRows 与 SQLite 侧的同名用例对仗：部署前跑
// `ppts migrate` 时，0053 是在「库里已经有老讲稿」的表上加一列，而不是空库建表。
// CI 的 prepare database / migration replay 两步都是空库路径，恰好绕过了这一路。
//
// PG 比 SQLite 多一个坑：jsonb 的 'null' 与 SQL NULL 是两样东西。前者 `IS NULL`
// 为假，且会被 loadSegmentsTx 反序列化成一个「指向空形状」的 *FocusTarget；
// 后者才是「这段不指任何东西」。所以这里不只查列在不在，还查旧行到底是哪一种。
//
// 搬库而不复刻 SQL：在事务里建一张与 loadSegmentsTx 的 SELECT 对齐、唯独没有
// focus 列的临时表，SET LOCAL search_path 让 0053 原文里的裸表名落到它身上。
// 于是跑的是内嵌迁移的逐字文本，断言也走产品自己的读路径，且回滚后 CI 库
// 不留痕迹——不需要给 ppts_app 额外授权，也不与同包其它用例抢真实表。
func TestPGFocusMigrationKeepsExistingRows(t *testing.T) {
	dsn := os.Getenv("PPTS_TEST_DATABASE")
	if dsn == "" {
		t.Skip("PPTS_TEST_DATABASE not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	sqlText, err := fs.ReadFile(migrations.FS, pgFocusMigrationFile)
	if err != nil {
		t.Fatalf("read embedded %s: %v", pgFocusMigrationFile, err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE narration_segments (
			script_id      text NOT NULL,
			segment_id     text NOT NULL,
			display_text   text NOT NULL DEFAULT '',
			spoken_text    text NOT NULL DEFAULT '',
			source_refs    text[] NOT NULL DEFAULT '{}',
			source_anchors jsonb NOT NULL DEFAULT '[]'::jsonb,
			status         text NOT NULL DEFAULT 'draft'
		) ON COMMIT DROP`); err != nil {
		t.Fatalf("create pre-migration table: %v", err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL search_path TO pg_temp, public`); err != nil {
		t.Fatalf("set search_path: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO narration_segments
			(script_id, segment_id, display_text, spoken_text, source_refs, source_anchors, status)
		VALUES
			('script-legacy', 'seg-01', '第一段', '第一段', ARRAY['slide-01/shape-1'], '[]'::jsonb, 'draft'),
			('script-legacy', 'seg-02', '第二段', '第二段', ARRAY['slide-01/shape-2'], '[]'::jsonb, 'draft')`); err != nil {
		t.Fatalf("insert legacy rows: %v", err)
	}

	// 起点确认：加列之前没有 focus。用 pg_attribute 查而不是 SELECT focus——
	// 后者会报错，PG 事务一旦 abort 后面全跑不下去。
	var colsBefore int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM pg_attribute
		  WHERE attrelid = 'narration_segments'::regclass AND attname = 'focus' AND NOT attisdropped`).Scan(&colsBefore); err != nil {
		t.Fatalf("count focus columns before: %v", err)
	}
	if colsBefore != 0 {
		t.Fatalf("起点不是「0053 之前」的表：focus 列已存在 %d 次", colsBefore)
	}

	// 应用内嵌迁移的逐字文本。
	if _, err := tx.Exec(ctx, string(sqlText)); err != nil {
		t.Fatalf("apply %s: %v", pgFocusMigrationFile, err)
	}

	var typ string
	if err := tx.QueryRow(ctx,
		`SELECT format_type(atttypid, atttypmod) FROM pg_attribute
		  WHERE attrelid = 'narration_segments'::regclass AND attname = 'focus' AND NOT attisdropped`).Scan(&typ); err != nil {
		t.Fatalf("read focus type: %v", err)
	}
	if typ != "jsonb" {
		t.Fatalf("focus 列类型应为 jsonb，实得 %q", typ)
	}

	var rows, nulls, notNulls int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM narration_segments`).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM narration_segments WHERE focus IS NULL`).Scan(&nulls); err != nil {
		t.Fatalf("count null focus: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM narration_segments WHERE focus IS NOT NULL`).Scan(&notNulls); err != nil {
		t.Fatalf("count non-null focus: %v", err)
	}
	if rows != 2 || nulls != 2 || notNulls != 0 {
		t.Fatalf("加列后旧行应仍是 2 行且 focus 全是 SQL NULL，实得 rows=%d nulls=%d notNulls=%d；"+
			"若 notNulls>0 且值是 jsonb 'null' 或 '{}'，下游会把「不指」读成「指向一个空形状」", rows, nulls, notNulls)
	}

	// 走产品自己的读路径：老讲稿要能读出来，且每段 Focus 都是 nil。
	segs, err := loadSegmentsTx(ctx, tx, "script-legacy")
	if err != nil {
		t.Fatalf("load legacy segments: %v", err)
	}
	if len(segs) != 2 {
		t.Fatalf("读回 %d 段，应为 2", len(segs))
	}
	for _, seg := range segs {
		if seg.Focus != nil {
			t.Fatalf("旧分段 %q 的 Focus 应为 nil，实得 %+v", seg.SegmentID, *seg.Focus)
		}
	}

	// 再跑一次是空操作（部署重试 / CI 的 migration replay 都会重放全部文件）。
	if _, err := tx.Exec(ctx, string(sqlText)); err != nil {
		t.Fatalf("replay %s: %v", pgFocusMigrationFile, err)
	}
	var rowsAgain, nullsAgain int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM narration_segments`).Scan(&rowsAgain); err != nil {
		t.Fatalf("count rows after replay: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM narration_segments WHERE focus IS NULL`).Scan(&nullsAgain); err != nil {
		t.Fatalf("count null focus after replay: %v", err)
	}
	if rowsAgain != rows || nullsAgain != nulls {
		t.Fatalf("重放迁移不应改动旧行：rows %d→%d, nulls %d→%d", rows, rowsAgain, nulls, nullsAgain)
	}
}
