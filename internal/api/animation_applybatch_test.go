package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/animation/animdefault"
	"github.com/F31/ppts/internal/animation/animstore"
	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/internal/project"
)

const (
	batchURL   = "/api/projects/" + anTestProject + "/animation/apply-batch?sourceRevisionNo=1&language=zh-CN"
	restoreURL = "/api/projects/" + anTestProject + "/animation/apply-batch/restore?sourceRevisionNo=1&language=zh-CN"
)

func newBatchServer(t *testing.T, ids []string, store animstore.Store, pages AnimationPageResolver) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	auth := func(h http.Handler) http.Handler {
		return AuthMiddlewareWithOptions(h, AuthOptions{
			LocalPrincipal: &Principal{TenantID: db.LocalTenantID, UserID: db.LocalUserID},
		})
	}
	NewAnimationHandler(store, pages, true).WithDeck(&fakeDeckResolver{ids: ids}).Register(mux, auth)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func batchKeyFor(slideID string) animation.Key {
	return animation.Key{
		TenantID: db.LocalTenantID, ProjectID: anTestProject, SlideID: slideID,
		Language: "zh-CN", SourceRevisionNo: 1,
	}
}

// seedSourceSpec 借已有的 apply-all 给源页造一份真实规格（走完整的默认规格编译链），
// 比手搓 DTO 更接近用户"先调好一页，再套用到别的页"的实际路径。
func seedSourceSpec(t *testing.T, store animstore.Store, slideID string) {
	t.Helper()
	ts := newBatchServer(t, []string{slideID}, store, &applyAllTestResolver{})
	status, payload := doJSON(t, http.MethodPost, ts.URL+applyAllURL, map[string]any{"presetId": "launch-bold"})
	if status != http.StatusOK {
		t.Fatalf("seed apply-all status = %d (%v)", status, payload)
	}
	rev, err := store.Get(context.Background(), batchKeyFor(slideID))
	if err != nil || len(rev.Tracks) == 0 {
		t.Fatalf("源页规格没造出来: rev=%+v err=%v", rev, err)
	}
}

func TestApplyBatchAppliesSourceStyleToSelectedSlides(t *testing.T) {
	store := newAnimationTestStore(t)
	seedSourceSpec(t, store, "slide-1")
	ts := newBatchServer(t, []string{"slide-1", "slide-2", "slide-3"}, store, &applyAllTestResolver{})

	status, payload := doJSON(t, http.MethodPost, ts.URL+batchURL, map[string]any{
		"sourceSlideId": "slide-1", "slideIds": []string{"slide-2", "slide-3"},
	})
	if status != http.StatusOK {
		t.Fatalf("apply-batch status = %d, want 200 (%v)", status, payload)
	}
	if payload["total"] != float64(2) || payload["applied"] != float64(2) {
		t.Fatalf("汇总不对: %v", payload)
	}
	previous, ok := payload["previous"].([]any)
	if !ok || len(previous) != 2 {
		t.Fatalf("previous 快照缺失: %v", payload["previous"])
	}
	for _, raw := range previous {
		snap := raw.(map[string]any)
		if snap["existed"] != false {
			t.Fatalf("原本没有规格的页应标 existed=false: %v", snap)
		}
	}
	for _, id := range []string{"slide-2", "slide-3"} {
		rev, err := store.Get(context.Background(), batchKeyFor(id))
		if err != nil {
			t.Fatalf("%s 没落库: %v", id, err)
		}
		if rev.PresetID != "launch-bold" || rev.Revision != 1 || len(rev.Tracks) == 0 {
			t.Fatalf("%s 规格与源页不一致: preset=%s rev=%d tracks=%d", id, rev.PresetID, rev.Revision, len(rev.Tracks))
		}
	}
	// 源页不应被改动。
	source, _ := store.Get(context.Background(), batchKeyFor("slide-1"))
	if source.Revision != 1 {
		t.Fatalf("源页 revision 被改成了 %d", source.Revision)
	}
}

// 一页不合格 → 整单不写。这是"全成或全不成"在 API 层的第一道闸。
func TestApplyBatchIneligiblePageWritesNothing(t *testing.T) {
	store := newAnimationTestStore(t)
	seedSourceSpec(t, store, "slide-1")
	pages := &applyAllTestResolver{noDuration: map[string]bool{"slide-3": true}}
	ts := newBatchServer(t, []string{"slide-1", "slide-2", "slide-3"}, store, pages)

	status, payload := doJSON(t, http.MethodPost, ts.URL+batchURL, map[string]any{
		"sourceSlideId": "slide-1", "slideIds": []string{"slide-2", "slide-3"},
	})
	if status != http.StatusUnprocessableEntity || payload["code"] != "batch_ineligible" {
		t.Fatalf("status = %d payload = %v", status, payload)
	}
	results := applyAllResultOf(t, payload)
	if results["slide-3"].Reason != batchReasonNoDur {
		t.Fatalf("slide-3 的原因不对: %+v", results["slide-3"])
	}
	// 零写入的响应里不能有任何一页标成 applied：那等于告诉用户"这几页套好了"。
	for id, res := range results {
		if res.Status == "applied" {
			t.Fatalf("整单被拒但 %s 标成了 applied", id)
		}
	}
	if results["slide-2"].Reason != batchReasonRejected {
		t.Fatalf("合格页应说明是因整单被拒而未写入: %+v", results["slide-2"])
	}
	for _, id := range []string{"slide-2", "slide-3"} {
		if _, err := store.Get(context.Background(), batchKeyFor(id)); !errors.Is(err, animation.ErrNotFound) {
			t.Fatalf("%s 不应被写入，Get err = %v", id, err)
		}
	}
}

// locked 是审批终态，overwrite=true 也不能越过。
func TestApplyBatchNeverOverwritesLockedPage(t *testing.T) {
	ctx := context.Background()
	store := newAnimationTestStore(t)
	seedSourceSpec(t, store, "slide-1")
	seedSourceSpec(t, store, "slide-2")
	if _, err := store.SetStatus(ctx, batchKeyFor("slide-2"), animation.StatusApproved); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := store.SetStatus(ctx, batchKeyFor("slide-2"), animation.StatusLocked); err != nil {
		t.Fatalf("lock: %v", err)
	}
	ts := newBatchServer(t, []string{"slide-1", "slide-2", "slide-3"}, store, &applyAllTestResolver{})

	status, payload := doJSON(t, http.MethodPost, ts.URL+batchURL, map[string]any{
		"sourceSlideId": "slide-1", "slideIds": []string{"slide-2", "slide-3"}, "overwrite": true,
	})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d (%v)", status, payload)
	}
	if got := applyAllResultOf(t, payload)["slide-2"].Reason; got != batchReasonLocked {
		t.Fatalf("locked 页原因 = %q", got)
	}
	if _, err := store.Get(ctx, batchKeyFor("slide-3")); !errors.Is(err, animation.ErrNotFound) {
		t.Fatalf("整单应零写入，slide-3 err = %v", err)
	}
}

func TestApplyBatchRejectsMissingAndUnknownSlides(t *testing.T) {
	store := newAnimationTestStore(t)
	ts := newBatchServer(t, []string{"slide-1", "slide-2"}, store, &applyAllTestResolver{})

	status, payload := doJSON(t, http.MethodPost, ts.URL+batchURL, map[string]any{
		"sourceSlideId": "slide-1", "slideIds": []string{"slide-2"},
	})
	if status != http.StatusUnprocessableEntity || payload["code"] != "source_missing" {
		t.Fatalf("源页无规格: status=%d payload=%v", status, payload)
	}
	seedSourceSpec(t, store, "slide-1")
	status, payload = doJSON(t, http.MethodPost, ts.URL+batchURL, map[string]any{
		"sourceSlideId": "slide-1", "slideIds": []string{"slide-9"},
	})
	if status != http.StatusUnprocessableEntity || payload["code"] != "unknown_slide" {
		t.Fatalf("未知页: status=%d payload=%v", status, payload)
	}
	status, payload = doJSON(t, http.MethodPost, ts.URL+batchURL, map[string]any{
		"sourceSlideId": "slide-1", "slideIds": []string{"slide-1"},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("源页不能当目标页: status=%d payload=%v", status, payload)
	}
}

// 撤销把"原本没有规格"的页真的删回原样，且整批原子。
func TestApplyBatchRestoreUndoesWholeBatch(t *testing.T) {
	store := newAnimationTestStore(t)
	seedSourceSpec(t, store, "slide-1")
	ts := newBatchServer(t, []string{"slide-1", "slide-2", "slide-3"}, store, &applyAllTestResolver{})

	status, payload := doJSON(t, http.MethodPost, ts.URL+batchURL, map[string]any{
		"sourceSlideId": "slide-1", "slideIds": []string{"slide-2", "slide-3"},
	})
	if status != http.StatusOK {
		t.Fatalf("apply: %d %v", status, payload)
	}
	raw, err := json.Marshal(payload["previous"])
	if err != nil {
		t.Fatalf("marshal previous: %v", err)
	}
	var snapshots []batchSlideSnapshot
	if err := json.Unmarshal(raw, &snapshots); err != nil {
		t.Fatalf("unmarshal previous: %v", err)
	}
	status, payload = doJSON(t, http.MethodPost, ts.URL+restoreURL, map[string]any{"snapshots": snapshots})
	if status != http.StatusOK || payload["restored"] != float64(2) {
		t.Fatalf("restore: %d %v", status, payload)
	}
	for _, id := range []string{"slide-2", "slide-3"} {
		if _, err := store.Get(context.Background(), batchKeyFor(id)); !errors.Is(err, animation.ErrNotFound) {
			t.Fatalf("撤销后 %s 应回到无规格状态，err = %v", id, err)
		}
	}
}

// distinctAnchorResolver 让每页拥有**不重合**的形状 ID 与分段 ID。
// 真实 PPT 就是如此；此前所有夹具都给每页复用同一批 ID，于是「把源页轨道原样
// 复制到目标页」的实现在单测里全绿、在真实 22 页数据上每页都 validation_failed。
type distinctAnchorResolver struct{}

func (r *distinctAnchorResolver) Resolve(_ context.Context, _, _, _ string, _ int, slideID, _ string) (*project.Page, animation.PageContext, error) {
	page := animationTestPage()
	page.SlideID = slideID
	for _, shape := range page.Shapes {
		shape.ID = shape.ID + "-" + slideID
	}
	segID := "seg-1-" + slideID
	pc := animation.PageContext{
		DurationMS: 6000,
		FPS:        30,
		Elements:   animdefault.PageAnchors(page),
		SegmentIDs: []string{segID},
	}
	return page, pc, nil
}

// 回归：源页的元素 ID 在目标页不存在时，批量套用仍必须成功，且目标页轨道
// 锚定的是**目标页自己的元素**。这是 2026-11-08 真机验证抓到的缺陷。
func TestApplyBatchRegeneratesPerPageAnchors(t *testing.T) {
	store := newAnimationTestStore(t)
	pages := &distinctAnchorResolver{}
	ids := []string{"slide-1", "slide-2", "slide-3"}
	seed := newBatchServer(t, []string{"slide-1"}, store, pages)
	status, payload := doJSON(t, http.MethodPost, seed.URL+applyAllURL, map[string]any{"presetId": "launch-bold"})
	if status != http.StatusOK {
		t.Fatalf("seed apply-all status = %d (%v)", status, payload)
	}
	source, err := store.Get(context.Background(), batchKeyFor("slide-1"))
	if err != nil {
		t.Fatalf("读源页规格: %v", err)
	}
	if !strings.Contains(source.Tracks[0].ElementID, "slide-1") {
		t.Fatalf("夹具没生效，源页元素 ID = %q", source.Tracks[0].ElementID)
	}

	ts := newBatchServer(t, ids, store, pages)
	status, payload = doJSON(t, http.MethodPost, ts.URL+batchURL, map[string]any{
		"sourceSlideId": "slide-1", "slideIds": []string{"slide-2", "slide-3"},
	})
	if status != http.StatusOK || payload["applied"] != float64(2) {
		t.Fatalf("apply-batch 应在真实页锚点下成功: status=%d payload=%v", status, payload)
	}
	for _, id := range []string{"slide-2", "slide-3"} {
		rev, err := store.Get(context.Background(), batchKeyFor(id))
		if err != nil {
			t.Fatalf("%s 没落库: %v", id, err)
		}
		if rev.PresetID != "launch-bold" || len(rev.Tracks) == 0 {
			t.Fatalf("%s 规格不对: preset=%s tracks=%d", id, rev.PresetID, len(rev.Tracks))
		}
		for _, track := range rev.Tracks {
			if track.ElementID != "" && !strings.HasSuffix(track.ElementID, id) {
				t.Fatalf("%s 的轨道锚到了别页元素 %q（源页轨道被原样复制）", id, track.ElementID)
			}
		}
	}
}

func TestApplyBatchRestoreRejectsBadSnapshotWithoutWriting(t *testing.T) {
	store := newAnimationTestStore(t)
	seedSourceSpec(t, store, "slide-1")
	ts := newBatchServer(t, []string{"slide-1", "slide-2"}, store, &applyAllTestResolver{})
	status, _ := doJSON(t, http.MethodPost, ts.URL+restoreURL, map[string]any{
		"snapshots": []map[string]any{{"slideId": "slide-9", "existed": false}},
	})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("无效快照应整单拒绝，status = %d", status)
	}
}
