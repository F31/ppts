//go:build pg

package narration

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
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
		"TRUNCATE narration_segments, narration_scripts, jobs, job_steps, source_revisions, projects, tenants RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO tenants(id,name) VALUES ($1,$2) ON CONFLICT DO NOTHING", []any{nrTenant, "nr"}},
		{"INSERT INTO projects(id,tenant_id,owner_user,title) VALUES ($1,$2,$3,$3) ON CONFLICT DO NOTHING", []any{nrProject, nrTenant, "tester"}},
	} {
		if _, err := pool.Exec(context.Background(), q.sql, q.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
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

	rev, err := s.EnsureExists(ctx, nrTenant, nrProject, nrSlide, lang, ModeOriginal)
	if err != nil {
		t.Fatalf("EnsureExists: %v", err)
	}
	if rev.Revision != 0 {
		t.Fatalf("initial revision: %d", rev.Revision)
	}

	// 第一次编辑（expected=0）成功。
	upd, err := s.Update(ctx, nrTenant, nrProject, nrSlide, lang, 0,
		segs(seg("seg-01", "本页介绍 PCIe 5.0"), seg("seg-02", "采用 x16 通道")))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if upd.Revision != 1 || len(upd.Segments) != 2 {
		t.Fatalf("updated: %+v", upd)
	}

	// 并发冲突：用过期 expected=0 再写 → ErrConflict 返回最新版。
	_, err = s.Update(ctx, nrTenant, nrProject, nrSlide, lang, 0,
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
	upd2, err := s.Update(ctx, nrTenant, nrProject, nrSlide, lang, 1,
		segs(seg("seg-01", "修改后的第一段")))
	if err != nil {
		t.Fatalf("Update#2: %v", err)
	}
	if upd2.Revision != 2 || upd2.Segments[0].DisplayText != "修改后的第一段" {
		t.Fatalf("updated: %+v", upd2)
	}
}

func TestNarrationLockPreventsEdit(t *testing.T) {
	s := nrStore(t)
	ctx := context.Background()
	if _, err := s.EnsureExists(ctx, nrTenant, nrProject, nrSlide, lang, ModeOriginal); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, nrTenant, nrProject, nrSlide, lang, 0,
		segs(seg("seg-1", "draft content"))); err != nil {
		t.Fatal(err)
	}
	// draft → approved → locked
	if _, err := s.SetStatus(ctx, nrTenant, nrProject, nrSlide, lang, StatusApproved); err != nil {
		t.Fatalf("approve: %v", err)
	}
	locked, err := s.SetStatus(ctx, nrTenant, nrProject, nrSlide, lang, StatusLocked)
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	if locked.Status != StatusLocked {
		t.Fatalf("locked status: %+v", locked)
	}
	// 锁定后编辑被拒。
	if _, err := s.Update(ctx, nrTenant, nrProject, nrSlide, lang, 2,
		segs(seg("seg-1", "hack"))); !errors.Is(err, ErrLocked) {
		t.Fatalf("update locked: got %v, want ErrLocked", err)
	}
	// 状态机不允许从 locked 回退。
	if _, err := s.SetStatus(ctx, nrTenant, nrProject, nrSlide, lang, StatusApproved); err == nil {
		t.Fatalf("downgrade from locked should fail")
	}
}
