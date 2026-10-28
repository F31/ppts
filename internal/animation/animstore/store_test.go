package animstore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/migrations"
)

func newAnimationStore(t *testing.T) Store {
	t.Helper()
	ctx := context.Background()
	sqldb, err := db.OpenSQLite(ctx, filepath.Join(t.TempDir(), "ppts.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	fsys, err := migrations.SQLite()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if _, err := db.MigrateSQLite(ctx, sqldb, fsys); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.EnsureLocalIdentity(ctx, sqldb); err != nil {
		t.Fatalf("identity: %v", err)
	}
	return NewSQLiteStore(sqldb)
}

func TestSQLiteStoreSuite(t *testing.T) {
	runStoreSuite(t, newAnimationStore(t), db.LocalTenantID, "p-animation")
}

func sampleRevision(tenant, project, slide string, sourceRevisionNo int) *animation.Revision {
	return &animation.Revision{
		TenantID:         tenant,
		ProjectID:        project,
		SlideID:          slide,
		Language:         "zh-CN",
		SourceRevisionNo: sourceRevisionNo,
		Status:           animation.StatusDraft,
		PresetID:         "business-calm",
		Intensity:        1.0,
		Tracks: []*animation.Track{{
			ElementID: "shape-1",
			Kind:      animation.KindTitle,
			Clips: []*animation.Clip{{
				ID: "c1", Type: "slideIn", StartMs: 0, DurationMs: 600, Easing: "easeOut",
				Params: animation.Params{Direction: "up", Offset: 40}, Trigger: "onEnter",
			}},
		}},
	}
}

// runStoreSuite 对任何 Store 实现跑同一组契约用例。新增实现时只需再调一次，
// 避免"pg 对了 sqlite 错了"这类只在生产暴露的偏差。
func runStoreSuite(t *testing.T, s Store, tenantID, projectID string) {
	t.Helper()
	ctx := context.Background()

	t.Run("get missing returns animation.ErrNotFound", func(t *testing.T) {
		_, err := s.Get(ctx, animation.Key{TenantID: tenantID, ProjectID: projectID, SlideID: "slide-none", Language: "zh-CN"})
		if !errors.Is(err, animation.ErrNotFound) {
			t.Fatalf("got %v, want animation.ErrNotFound", err)
		}
	})

	t.Run("insert then update keeps revision monotonic", func(t *testing.T) {
		slide := "slide-insert"
		rev, err := s.Put(ctx, sampleRevision(tenantID, projectID, slide, 0), 0)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		if rev.Revision != 1 || rev.Status != animation.StatusDraft {
			t.Fatalf("after insert revision=%d status=%s, want 1/draft", rev.Revision, rev.Status)
		}
		if len(rev.Tracks) != 1 || rev.Tracks[0].Clips[0].Params.Direction != "up" {
			t.Fatalf("tracks not persisted: %+v", rev.Tracks)
		}

		next := sampleRevision(tenantID, projectID, slide, 0)
		next.PresetID = ""
		next.Intensity = 1.2
		next.Tracks[0].Clips[0].Params.Offset = 64
		updated, err := s.Put(ctx, next, 1)
		if err != nil {
			t.Fatalf("update: %v", err)
		}
		if updated.Revision != 2 {
			t.Fatalf("revision = %d, want 2", updated.Revision)
		}
		if updated.PresetID != "" || updated.Intensity != 1.2 {
			t.Fatalf("content not updated: %+v", updated)
		}
		if got := updated.Tracks[0].Clips[0].Params.Offset; got != 64 {
			t.Fatalf("offset = %v, want 64", got)
		}
	})

	t.Run("stale expected returns conflict with latest", func(t *testing.T) {
		slide := "slide-conflict"
		if _, err := s.Put(ctx, sampleRevision(tenantID, projectID, slide, 0), 0); err != nil {
			t.Fatalf("insert: %v", err)
		}
		if _, err := s.Put(ctx, sampleRevision(tenantID, projectID, slide, 0), 1); err != nil {
			t.Fatalf("update: %v", err)
		}
		_, err := s.Put(ctx, sampleRevision(tenantID, projectID, slide, 0), 1)
		var conflict *animation.ErrConflict
		if !errors.As(err, &conflict) {
			t.Fatalf("got %v, want *animation.ErrConflict", err)
		}
		if conflict.Latest == nil || conflict.Latest.Revision != 2 {
			t.Fatalf("conflict latest = %+v, want revision 2", conflict.Latest)
		}
	})

	t.Run("insert with non-zero expected conflicts", func(t *testing.T) {
		_, err := s.Put(ctx, sampleRevision(tenantID, projectID, "slide-ghost", 0), 3)
		var conflict *animation.ErrConflict
		if !errors.As(err, &conflict) {
			t.Fatalf("got %v, want *animation.ErrConflict", err)
		}
	})

	t.Run("locked spec rejects writes before revision check", func(t *testing.T) {
		slide := "slide-locked"
		if _, err := s.Put(ctx, sampleRevision(tenantID, projectID, slide, 0), 0); err != nil {
			t.Fatalf("insert: %v", err)
		}
		if _, err := s.SetStatus(ctx, animation.Key{TenantID: tenantID, ProjectID: projectID, SlideID: slide, Language: "zh-CN"}, animation.StatusApproved); err != nil {
			t.Fatalf("approve: %v", err)
		}
		if _, err := s.SetStatus(ctx, animation.Key{TenantID: tenantID, ProjectID: projectID, SlideID: slide, Language: "zh-CN"}, animation.StatusLocked); err != nil {
			t.Fatalf("lock: %v", err)
		}
		// 故意用过期的 expected：仍应报 animation.ErrLocked，而不是 animation.ErrConflict——
		// "不能写"优先于"你的版本旧了"，否则用户会去刷新重试，还是写不进去。
		if _, err := s.Put(ctx, sampleRevision(tenantID, projectID, slide, 0), 99); !errors.Is(err, animation.ErrLocked) {
			t.Fatalf("got %v, want animation.ErrLocked", err)
		}
	})

	t.Run("status machine rejects skips", func(t *testing.T) {
		slide := "slide-status"
		if _, err := s.Put(ctx, sampleRevision(tenantID, projectID, slide, 0), 0); err != nil {
			t.Fatalf("insert: %v", err)
		}
		key := animation.Key{TenantID: tenantID, ProjectID: projectID, SlideID: slide, Language: "zh-CN"}

		// draft → locked 跳级，必须拒绝。
		if _, err := s.SetStatus(ctx, key, animation.StatusLocked); !errors.Is(err, animation.ErrStatusTransition) {
			t.Fatalf("draft→locked: got %v, want animation.ErrStatusTransition", err)
		}
		if _, err := s.SetStatus(ctx, key, animation.StatusApproved); err != nil {
			t.Fatalf("draft→approved: %v", err)
		}
		if _, err := s.SetStatus(ctx, key, animation.StatusLocked); err != nil {
			t.Fatalf("approved→locked: %v", err)
		}
		unlocked, err := s.SetStatus(ctx, key, animation.StatusApproved)
		if err != nil {
			t.Fatalf("locked→approved: %v", err)
		}
		if unlocked.Status != animation.StatusApproved {
			t.Fatalf("status = %s, want approved", unlocked.Status)
		}
		// locked→draft 不是合法回退路径：要么先解锁再改，要么保持锁定。
		if _, err := s.SetStatus(ctx, key, animation.StatusDraft); !errors.Is(err, animation.ErrStatusTransition) {
			t.Fatalf("locked→draft: got %v, want animation.ErrStatusTransition", err)
		}
	})

	t.Run("legacy row is visible from newer source revisions", func(t *testing.T) {
		slide := "slide-legacy"
		if _, err := s.Put(ctx, sampleRevision(tenantID, projectID, slide, 0), 0); err != nil {
			t.Fatalf("insert legacy: %v", err)
		}
		got, err := s.Get(ctx, animation.Key{TenantID: tenantID, ProjectID: projectID, SlideID: slide, Language: "zh-CN", SourceRevisionNo: 7})
		if err != nil {
			t.Fatalf("get with newer revision: %v", err)
		}
		if got.SourceRevisionNo != 0 {
			t.Fatalf("fallback revision = %d, want 0", got.SourceRevisionNo)
		}

		// 新版本首次写入不继承旧版（元素锚点可能已失效），而是新建一行。
		fresh := sampleRevision(tenantID, projectID, slide, 7)
		fresh.Tracks = nil
		if _, err := s.Put(ctx, fresh, 0); err != nil {
			t.Fatalf("insert new revision: %v", err)
		}
		got, err = s.Get(ctx, animation.Key{TenantID: tenantID, ProjectID: projectID, SlideID: slide, Language: "zh-CN", SourceRevisionNo: 7})
		if err != nil {
			t.Fatalf("get revision 7: %v", err)
		}
		if got.SourceRevisionNo != 7 {
			t.Fatalf("exact match lost: got revision %d", got.SourceRevisionNo)
		}
		if got.Tracks == nil || len(got.Tracks) != 0 {
			t.Fatalf("nil tracks must round-trip as empty slice, got %#v", got.Tracks)
		}
	})

	t.Run("tenant isolation", func(t *testing.T) {
		slide := "slide-isolation"
		if _, err := s.Put(ctx, sampleRevision(tenantID, projectID, slide, 0), 0); err != nil {
			t.Fatalf("insert: %v", err)
		}
		otherTenant := "00000000-0000-0000-0000-00000000dead"
		if _, err := s.Get(ctx, animation.Key{TenantID: otherTenant, ProjectID: projectID, SlideID: slide, Language: "zh-CN"}); !errors.Is(err, animation.ErrNotFound) {
			t.Fatalf("cross tenant get: got %v, want animation.ErrNotFound", err)
		}
	})

	t.Run("invalid key is rejected", func(t *testing.T) {
		_, err := s.Get(ctx, animation.Key{TenantID: tenantID, ProjectID: projectID})
		if !errors.Is(err, animation.ErrInvalidKey) {
			t.Fatalf("got %v, want animation.ErrInvalidKey", err)
		}
	})
}
