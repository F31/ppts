package artifact

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/migrations"
)

func newSQLiteArtifactStore(t *testing.T) *SQLiteStore {
	t.Helper()
	ctx := context.Background()
	sqldb, err := db.OpenSQLite(ctx, filepath.Join(t.TempDir(), "ppts.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	fsys, _ := migrations.SQLite()
	if _, err := db.MigrateSQLite(ctx, sqldb, fsys); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.EnsureLocalIdentity(ctx, sqldb); err != nil {
		t.Fatalf("identity: %v", err)
	}
	return NewSQLiteStore(sqldb)
}

// TestSQLiteArtifactRoundTrip 守住"列清单顺序 == Scan 目标顺序"这一不可见约束。
//
// 背景：新增 revision_no/source_display_name 两列时，SQLite 侧把列追加在 created_at 之后，
// Scan 目标却插在 &created 之前 → SQLite profile 下 Create/Get/ListByProject 全部把值读进
// 错误字段（编译期不可见）。该包此前只有 PG 测试，故缺陷未被发现。
// 本用例对每个读路径都断言**易错列**的真实往返值，任何一侧顺序再次漂移都会立刻炸。
func TestSQLiteArtifactRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newSQLiteArtifactStore(t)
	const tenant = db.LocalTenantID
	const project = "p1"

	in := NewArtifact{
		ProjectID:         project,
		SnapshotHash:      "snap-1",
		Format:            FormatMP4,
		ObjectKey:         "t/p/narration-1/artifact/out.mp4",
		ContentHash:       "hash-1",
		SizeBytes:         4096,
		DurationMS:        12_345,
		TimelineKey:       "t/p/narration-1/timeline/abc.json",
		SourceRevisionNo:  7,
		SourceDisplayName: "年度汇报 v7",
	}
	created, err := s.Create(ctx, tenant, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	assertSourceMeta(t, "Create", created, in)

	got, err := s.Get(ctx, tenant, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	assertSourceMeta(t, "Get", got, in)

	byProject, err := s.ListByProject(ctx, tenant, project)
	if err != nil {
		t.Fatalf("list by project: %v", err)
	}
	if len(byProject) != 1 {
		t.Fatalf("list by project len=%d", len(byProject))
	}
	assertSourceMeta(t, "ListByProject", byProject[0], in)

	all, err := s.ListAll(ctx, tenant)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("list all len=%d", len(all))
	}
	assertSourceMeta(t, "ListAll", all[0], in)
}

// assertSourceMeta 校验往返保真，重点覆盖曾被顺序错位波及的四列：
// created_at(time.Time) / revision_no(int) / source_display_name(string) / duration_ms(int64)。
func assertSourceMeta(t *testing.T, path string, got *Artifact, want NewArtifact) {
	t.Helper()
	if got.SourceRevisionNo != want.SourceRevisionNo {
		t.Fatalf("%s: SourceRevisionNo=%d want %d", path, got.SourceRevisionNo, want.SourceRevisionNo)
	}
	if got.SourceDisplayName != want.SourceDisplayName {
		t.Fatalf("%s: SourceDisplayName=%q want %q", path, got.SourceDisplayName, want.SourceDisplayName)
	}
	if got.DurationMS != want.DurationMS {
		t.Fatalf("%s: DurationMS=%d want %d", path, got.DurationMS, want.DurationMS)
	}
	if got.TimelineKey != want.TimelineKey {
		t.Fatalf("%s: TimelineKey=%q want %q", path, got.TimelineKey, want.TimelineKey)
	}
	if got.CreatedAt.IsZero() {
		t.Fatalf("%s: CreatedAt not parsed (zero)", path)
	}
}
