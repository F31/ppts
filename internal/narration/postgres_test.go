//go:build pg

package narration

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/tenant"
)

const (
	nrTenant  = "00000000-0000-0000-0000-0000000000b1"
	nrProject = "00000000-0000-0000-0000-0000000000b2"
	nrSlide   = "slide-01"
	lang      = "zh-CN"
)

func nrStore(t *testing.T) *PGStore {
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
	if _, err := pool.Exec(context.Background(),
		"TRUNCATE narration_segments, narration_scripts, jobs, job_steps, source_revisions, projects, tenants CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		"INSERT INTO tenants(id,name) VALUES ($1,$2) ON CONFLICT DO NOTHING", nrTenant, "nr"); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if err := tenant.Run(context.Background(), pool, nrTenant, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			"INSERT INTO projects(id,tenant_id,owner_user,title) VALUES ($1,$2,$3,$3) ON CONFLICT DO NOTHING",
			nrProject, nrTenant, "tester")
		return err
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return NewPGStore(pool)
}

func segs(n ...struct{ id, display string }) []*Segment {
	var out []*Segment
	for _, s := range n {
		out = append(out, &Segment{SegmentID: s.id, DisplayText: s.display, SpokenText: s.display, SourceRefs: []string{"slide-01/shape-1"}})
	}
	return out
}

func seg(id, display string) struct{ id, display string } {
	return struct{ id, display string }{id, display}
}

func TestNarrationUpdateAndConflict(t *testing.T) {
	s := nrStore(t)
	ctx := context.Background()

	rev, err := s.EnsureExists(ctx, nrTenant, nrProject, 0, nrSlide, lang, ModeOriginal)
	if err != nil {
		t.Fatalf("EnsureExists: %v", err)
	}
	if rev.Revision != 0 {
		t.Fatalf("initial revision: %d", rev.Revision)
	}

	// 第一次编辑（expected=0）成功。
	upd, err := s.Update(ctx, nrTenant, nrProject, 0, nrSlide, lang, 0,
		segs(seg("seg-01", "本页介绍 PCIe 5.0"), seg("seg-02", "采用 x16 通道")))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if upd.Revision != 1 || len(upd.Segments) != 2 {
		t.Fatalf("updated: %+v", upd)
	}

	// 并发冲突：用过期 expected=0 再写 → ErrConflict 返回最新版。
	_, err = s.Update(ctx, nrTenant, nrProject, 0, nrSlide, lang, 0,
		segs(seg("seg-01", "覆盖写入")))
	var conflict *ErrConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	if conflict.Latest == nil || conflict.Latest.Revision != 1 {
		t.Fatalf("conflict latest: %+v", conflict.Latest)
	}
	if len(conflict.Latest.Segments) != 2 {
		t.Fatalf("conflict latest segments lost: %+v", conflict.Latest.Segments)
	}

	// 正确 expected 后再次更新成功。
	upd2, err := s.Update(ctx, nrTenant, nrProject, 0, nrSlide, lang, 1,
		segs(seg("seg-01", "修改后的第一段")))
	if err != nil {
		t.Fatalf("Update#2: %v", err)
	}
	if upd2.Revision != 2 || upd2.Segments[0].DisplayText != "修改后的第一段" {
		t.Fatalf("updated: %+v", upd2)
	}
}

func TestNarrationLockedScriptDirectEdit(t *testing.T) {
	s := nrStore(t)
	ctx := context.Background()
	if _, err := s.EnsureExists(ctx, nrTenant, nrProject, 0, nrSlide, lang, ModeOriginal); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, nrTenant, nrProject, 0, nrSlide, lang, 0,
		segs(seg("seg-1", "draft content"))); err != nil {
		t.Fatal(err)
	}
	// draft → approved → locked
	if _, err := s.SetStatus(ctx, nrTenant, nrProject, 0, nrSlide, lang, StatusApproved); err != nil {
		t.Fatalf("approve: %v", err)
	}
	locked, err := s.SetStatus(ctx, nrTenant, nrProject, 0, nrSlide, lang, StatusLocked)
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	if locked.Status != StatusLocked {
		t.Fatalf("locked status: %+v", locked)
	}
	// Revision protection remains in force for legacy locked scripts.
	_, err = s.Update(ctx, nrTenant, nrProject, 0, nrSlide, lang, locked.Revision-1, segs(seg("seg-1", "stale")))
	var conflict *ErrConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("stale update: got %v, want ErrConflict", err)
	}
	edited, err := s.Update(ctx, nrTenant, nrProject, 0, nrSlide, lang, locked.Revision, segs(seg("seg-1", "new draft")))
	if err != nil || edited.Status != StatusDraft || edited.Revision != locked.Revision+1 {
		t.Fatalf("direct edit: %+v, %v", edited, err)
	}
	// 锁定后可回到 approved，供用户重新编辑并重新生成语音。
	unlocked, err := s.SetStatus(ctx, nrTenant, nrProject, 0, nrSlide, lang, StatusApproved)
	if err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if unlocked.Status != StatusApproved {
		t.Fatalf("unlocked status: %+v", unlocked)
	}
}

func TestNarrationUpdateStoresAndPreservesSourceAnchors(t *testing.T) {
	s := nrStore(t)
	ctx := context.Background()
	if _, err := s.EnsureExists(ctx, nrTenant, nrProject, 0, nrSlide, lang, ModePolish); err != nil {
		t.Fatal(err)
	}
	anchors := []SourceAnchor{{SlideID: nrSlide, ShapeID: "shape-1", Kind: "shape_text", Raw: "PCIe 5.0", Confidence: 1}}
	upd, err := s.Update(ctx, nrTenant, nrProject, 0, nrSlide, lang, 0, []*Segment{{
		SegmentID: "seg-01", DisplayText: "介绍 PCIe 5.0", SpokenText: "介绍 PCIe 5.0",
		SourceRefs: []string{nrSlide + "/shape-1"}, SourceAnchors: anchors,
	}})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(upd.Segments[0].SourceAnchors) != 1 || upd.Segments[0].SourceAnchors[0].ShapeID != "shape-1" {
		t.Fatalf("anchors not stored: %+v", upd.Segments[0].SourceAnchors)
	}

	// User-facing Update sends nil anchors; store preserves server provenance by segment_id.
	upd2, err := s.Update(ctx, nrTenant, nrProject, 0, nrSlide, lang, upd.Revision, []*Segment{{
		SegmentID: "seg-01", DisplayText: "用户修改 PCIe 5.0", SpokenText: "用户修改 PCIe 5.0",
		SourceRefs: []string{nrSlide + "/shape-1"},
	}})
	if err != nil {
		t.Fatalf("Update preserve: %v", err)
	}
	if len(upd2.Segments[0].SourceAnchors) != 1 || upd2.Segments[0].SourceAnchors[0].Raw != "PCIe 5.0" {
		t.Fatalf("anchors not preserved: %+v", upd2.Segments[0].SourceAnchors)
	}
}

// TestNarrationListByProjectReturnsSegments 回归守护：pgx 单连接下，ListByProject 必须
// 先关闭外层游标再加载分段，否则报 "conn busy"（此前 /projects/{pid}/scripts 返回 500）。
func TestNarrationListByProjectReturnsSegments(t *testing.T) {
	s := nrStore(t)
	ctx := context.Background()
	const rev = 5
	if _, err := s.EnsureExists(ctx, nrTenant, nrProject, rev, nrSlide, lang, ModePolish); err != nil {
		t.Fatalf("EnsureExists: %v", err)
	}
	if _, err := s.Update(ctx, nrTenant, nrProject, rev, nrSlide, lang, 0,
		segs(seg("seg-01", "第一段"), seg("seg-02", "第二段"))); err != nil {
		t.Fatalf("Update: %v", err)
	}
	list, err := s.ListByProject(ctx, nrTenant, nrProject, rev, lang)
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(list) != 1 || len(list[0].Segments) != 2 || list[0].Segments[0].DisplayText != "第一段" {
		t.Fatalf("list = %+v", list)
	}
}
