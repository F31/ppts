package narration

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/migrations"
)

// newNarrationStoreBench 是 newNarrationStore 的 testing.TB 版本（benchmark 用）。
func newNarrationStoreBench(tb testing.TB) *SQLiteStore {
	tb.Helper()
	ctx := context.Background()
	sqldb, err := db.OpenSQLite(ctx, filepath.Join(tb.TempDir(), "ppts.db"))
	if err != nil {
		tb.Fatalf("open: %v", err)
	}
	tb.Cleanup(func() { _ = sqldb.Close() })
	fsys, _ := migrations.SQLite()
	if _, err := db.MigrateSQLite(ctx, sqldb, fsys); err != nil {
		tb.Fatalf("migrate: %v", err)
	}
	if err := db.EnsureLocalIdentity(ctx, sqldb); err != nil {
		tb.Fatalf("identity: %v", err)
	}
	return NewSQLiteStore(sqldb)
}

// narrationSeeder 是播种只需要的那两个写入口（SQLite / PG 都满足）。
type narrationSeeder interface {
	EnsureExists(context.Context, string, string, int, string, string, ScriptMode) (*Revision, error)
	Update(context.Context, string, string, int, string, string, int64, []*Segment) (*Revision, error)
}

// seedNarrationSlides 造 n 页、每页 2 段，供列表读的基准与查询数门禁用。
func seedNarrationSlides(tb testing.TB, s narrationSeeder, tenantID, projectID string, rev, n int) {
	tb.Helper()
	seedNarrationSlidesRange(tb, s, tenantID, projectID, rev, 0, n)
}

// seedNarrationSlidesRange 只造 [from, to) 这几页：已有页再 Update(expected=0) 会撞乐观锁，
// 所以「先造 3 页量一次、再造到 12 页」必须从第 3 页续。
func seedNarrationSlidesRange(tb testing.TB, s narrationSeeder, tenantID, projectID string, rev, from, to int) {
	tb.Helper()
	ctx := context.Background()
	for i := from; i < to; i++ {
		slide := fmt.Sprintf("slide-%02d", i+1)
		if _, err := s.EnsureExists(ctx, tenantID, projectID, rev, slide, "zh-CN", ModePolish); err != nil {
			tb.Fatalf("EnsureExists %s: %v", slide, err)
		}
		if _, err := s.Update(ctx, tenantID, projectID, rev, slide, "zh-CN", 0,
			benchSegments()); err != nil {
			tb.Fatalf("Update %s: %v", slide, err)
		}
	}
}

// benchSegments 两段讲稿（benchmark 与查询数门禁共用；pg 侧的同名构造在 postgres_test.go）。
func benchSegments() []*Segment {
	return []*Segment{
		{SegmentID: "seg-01", DisplayText: "第一段", SpokenText: "第一段"},
		{SegmentID: "seg-02", DisplayText: "第二段", SpokenText: "第二段"},
	}
}

// BenchmarkListByProject30 是一次 30 页编辑器加载的读侧成本。
// 逐页查分段时是 31 次查询；批量后固定 2 次。改动前后都用这个基准复测。
func BenchmarkListByProject30(b *testing.B) {
	ctx := context.Background()
	s := newNarrationStoreBench(b)
	const tenant, project = db.LocalTenantID, "p1"
	seedNarrationSlides(b, s, tenant, project, 5, 30)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		list, err := s.ListByProject(ctx, tenant, project, 5, "zh-CN")
		if err != nil {
			b.Fatal(err)
		}
		if len(list) != 30 {
			b.Fatalf("list len = %d", len(list))
		}
	}
}
