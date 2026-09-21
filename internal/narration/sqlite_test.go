package narration

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/migrations"
)

func newNarrationStore(t *testing.T) *SQLiteStore {
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

func TestSQLiteNarrationLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newNarrationStore(t)
	const tenant = db.LocalTenantID
	const project, slide, lang = "p1", "slide-1", "zh-CN"

	if _, err := s.Get(ctx, tenant, project, slide, lang); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get missing should be not found, got %v", err)
	}

	rev, err := s.EnsureExists(ctx, tenant, project, slide, lang, ModeOriginal)
	if err != nil || rev.Revision != 0 {
		t.Fatalf("ensure: err=%v rev=%+v", err, rev)
	}
	// 幂等
	rev2, err := s.EnsureExists(ctx, tenant, project, slide, lang, ModeOriginal)
	if err != nil || rev2.ID != rev.ID {
		t.Fatalf("ensure idempotent: err=%v id=%s want=%s", err, rev2.ID, rev.ID)
	}

	// Update 分段（含 source_refs / source_anchors）
	segs := []*Segment{{
		SegmentID: "seg-1", DisplayText: "显示", SpokenText: "朗读",
		SourceRefs:    []string{"shape-1"},
		SourceAnchors: []SourceAnchor{{SlideID: slide, ShapeID: "shape-1", Kind: "body"}},
	}}
	updated, err := s.Update(ctx, tenant, project, slide, lang, 0, segs)
	if err != nil || updated.Revision != 1 || len(updated.Segments) != 1 {
		t.Fatalf("update: err=%v rev=%+v", err, updated)
	}
	if updated.Segments[0].SpokenText != "朗读" || len(updated.Segments[0].SourceRefs) != 1 {
		t.Fatalf("segment roundtrip failed: %+v", updated.Segments[0])
	}
	if len(updated.Segments[0].SourceAnchors) != 1 {
		t.Fatalf("anchors roundtrip failed: %+v", updated.Segments[0])
	}

	// 乐观锁冲突
	if _, err := s.Update(ctx, tenant, project, slide, lang, 0, segs); err == nil {
		t.Fatal("stale expected_revision should conflict")
	} else {
		var conflict *ErrConflict
		if !errors.As(err, &conflict) {
			t.Fatalf("want ErrConflict, got %T %v", err, err)
		}
		if conflict.Latest == nil || conflict.Latest.Revision != 1 {
			t.Fatalf("conflict latest mismatch: %+v", conflict.Latest)
		}
	}

	// 状态流转 draft → approved → locked
	if _, err := s.SetStatus(ctx, tenant, project, slide, lang, StatusApproved); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := s.SetStatus(ctx, tenant, project, slide, lang, StatusLocked); err != nil {
		t.Fatalf("lock: %v", err)
	}
	// Legacy locks do not require a separate unlock before editing.
	if _, err := s.Update(ctx, tenant, project, slide, lang, 0, segs); err == nil {
		t.Fatal("locked script must still reject a stale revision")
	}
	edited, err := s.Update(ctx, tenant, project, slide, lang, 1, segs)
	if err != nil || edited.Status != StatusDraft || edited.Revision != 2 {
		t.Fatalf("direct edit of locked script: %+v, %v", edited, err)
	}
	if unlocked, err := s.SetStatus(ctx, tenant, project, slide, lang, StatusApproved); err != nil || unlocked.Status != StatusApproved {
		t.Fatalf("unlock = %+v, %v", unlocked, err)
	}

	// CountDraftSegments（已锁定稿分段状态仍为 draft：整页替换时写入 draft）
	n, err := s.CountDraftSegments(ctx, tenant, project)
	if err != nil || n != 1 {
		t.Fatalf("count draft: err=%v n=%d", err, n)
	}

	// MarkAudioRevision
	if err := s.MarkAudioRevision(ctx, tenant, project, slide, lang, 1); err != nil {
		t.Fatalf("mark audio: %v", err)
	}
	list, err := s.ListByProject(ctx, tenant, project, lang)
	if err != nil || len(list) != 1 || list[0].AudioRevision != 1 || len(list[0].Segments) != 1 {
		t.Fatalf("list by project: err=%v list=%+v", err, list)
	}
	if list[0].AudioRevision >= list[0].Revision {
		t.Fatal("editing must leave the previous audio marked stale")
	}
}
