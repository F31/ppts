package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/animation/animdefault"
	"github.com/F31/ppts/internal/animation/animstore"
	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/internal/project"
)

const applyAllURL = "/api/projects/" + anTestProject + "/animation/apply-all?sourceRevisionNo=1&language=zh-CN"

type fakeDeckResolver struct{ ids []string }

func (f *fakeDeckResolver) SlideIDs(context.Context, string, string, string, int) ([]string, error) {
	return f.ids, nil
}

// applyAllTestResolver 让每页可以有不同的上下文（尤其是「这页还没配音」）。
type applyAllTestResolver struct {
	// noDuration 的页返回 DurationMS=0，模拟整份里有个别页没配音。
	noDuration map[string]bool
}

func (r *applyAllTestResolver) Resolve(_ context.Context, _, _, _ string, _ int, slideID, _ string) (*project.Page, animation.PageContext, error) {
	page := animationTestPage()
	page.SlideID = slideID
	pc := animation.PageContext{
		DurationMS: 6000,
		FPS:        30,
		Elements:   animdefault.PageAnchors(page),
		SegmentIDs: []string{"seg-1"},
	}
	if r.noDuration[slideID] {
		pc.DurationMS = 0
	}
	return page, pc, nil
}

func newApplyAllServer(t *testing.T, ids []string, noDuration map[string]bool) (*httptest.Server, animstore.Store) {
	t.Helper()
	store := newAnimationTestStore(t)
	mux := http.NewServeMux()
	auth := func(h http.Handler) http.Handler {
		return AuthMiddlewareWithOptions(h, AuthOptions{
			LocalPrincipal: &Principal{TenantID: db.LocalTenantID, UserID: db.LocalUserID},
		})
	}
	NewAnimationHandler(store, &applyAllTestResolver{noDuration: noDuration}, true).
		WithDeck(&fakeDeckResolver{ids: ids}).
		Register(mux, auth)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, store
}

func applyAllResultOf(t *testing.T, payload map[string]any) map[string]applyAllSlideResult {
	t.Helper()
	raw, ok := payload["results"].([]any)
	if !ok {
		t.Fatalf("results 缺失或类型不对: %v", payload)
	}
	out := map[string]applyAllSlideResult{}
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("result 元素不是对象: %v", item)
		}
		slideID, _ := m["slideId"].(string)
		status, _ := m["status"].(string)
		reason, _ := m["reason"].(string)
		out[slideID] = applyAllSlideResult{SlideID: slideID, Status: status, Reason: reason}
	}
	return out
}

func TestApplyAllWritesEverySlide(t *testing.T) {
	ts, store := newApplyAllServer(t, []string{"slide-1", "slide-2", "slide-3"}, nil)

	status, payload := doJSON(t, http.MethodPost, ts.URL+applyAllURL, map[string]any{"presetId": "launch-bold"})
	if status != http.StatusOK {
		t.Fatalf("apply-all status = %d, want 200 (%v)", status, payload)
	}
	if payload["total"] != float64(3) || payload["applied"] != float64(3) || payload["skipped"] != float64(0) {
		t.Fatalf("汇总不对: %v", payload)
	}
	results := applyAllResultOf(t, payload)
	for _, id := range []string{"slide-1", "slide-2", "slide-3"} {
		if results[id].Status != "applied" {
			t.Fatalf("%s 未应用: %+v", id, results[id])
		}
		rev, err := store.Get(context.Background(), animation.Key{
			TenantID: db.LocalTenantID, ProjectID: anTestProject, SlideID: id,
			Language: "zh-CN", SourceRevisionNo: 1,
		})
		if err != nil {
			t.Fatalf("%s 没落库: %v", id, err)
		}
		if rev.PresetID != "launch-bold" || rev.Revision != 1 || len(rev.Tracks) == 0 {
			t.Fatalf("%s 规格不对: preset=%s rev=%d tracks=%d", id, rev.PresetID, rev.Revision, len(rev.Tracks))
		}
	}
}

func TestApplyAllSkipsManualEditsAndIsIdempotent(t *testing.T) {
	ts, store := newApplyAllServer(t, []string{"slide-1", "slide-2"}, nil)

	// 先给 slide-1 存一份「人手改过 trigger」的规格。validPutBody 的 clip 不带来源，
	// 这里显式补 manual —— 这正是 ReconcileTriggerSources 在真实编辑里会写下的标记。
	body := validPutBody(0)
	track := body["tracks"].([]map[string]any)[0]
	track["clips"].([]map[string]any)[0]["triggerSource"] = animation.TriggerSourceManual
	manualURL := "/api/projects/" + anTestProject + "/slides/slide-1/animation?sourceRevisionNo=1&language=zh-CN"
	status, payload := doJSON(t, http.MethodPut, ts.URL+manualURL, body)
	if status != http.StatusOK {
		t.Fatalf("准备人工规格失败: %d %v", status, payload)
	}
	before, err := store.Get(context.Background(), animation.Key{
		TenantID: db.LocalTenantID, ProjectID: anTestProject, SlideID: "slide-1",
		Language: "zh-CN", SourceRevisionNo: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	status, payload = doJSON(t, http.MethodPost, ts.URL+applyAllURL, map[string]any{"presetId": "launch-bold"})
	if status != http.StatusOK || payload["applied"] != float64(1) || payload["skipped"] != float64(1) {
		t.Fatalf("应只应用 1 页: %d %v", status, payload)
	}
	results := applyAllResultOf(t, payload)
	if results["slide-1"].Reason != "manual_edits" || results["slide-2"].Status != "applied" {
		t.Fatalf("跳过原因不对: %+v", results)
	}

	// 再跑一次：人工页仍然不被覆盖（一键动作可以重复点，不会蚕食人工成果）。
	_, payload = doJSON(t, http.MethodPost, ts.URL+applyAllURL, map[string]any{"presetId": "business-calm"})
	results = applyAllResultOf(t, payload)
	if results["slide-1"].Reason != "manual_edits" {
		t.Fatalf("重复应用把人工页覆盖了: %+v", results)
	}
	after, err := store.Get(context.Background(), animation.Key{
		TenantID: db.LocalTenantID, ProjectID: anTestProject, SlideID: "slide-1",
		Language: "zh-CN", SourceRevisionNo: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision || after.PresetID != before.PresetID {
		t.Fatalf("人工规格被改动: before=%d/%s after=%d/%s",
			before.Revision, before.PresetID, after.Revision, after.PresetID)
	}
}

func TestApplyAllOverwriteReclaimsManualEdits(t *testing.T) {
	ts, _ := newApplyAllServer(t, []string{"slide-1"}, nil)
	body := validPutBody(0)
	body["tracks"].([]map[string]any)[0]["clips"].([]map[string]any)[0]["triggerSource"] = animation.TriggerSourceManual
	manualURL := "/api/projects/" + anTestProject + "/slides/slide-1/animation?sourceRevisionNo=1&language=zh-CN"
	if status, payload := doJSON(t, http.MethodPut, ts.URL+manualURL, body); status != http.StatusOK {
		t.Fatalf("准备人工规格失败: %d %v", status, payload)
	}

	status, payload := doJSON(t, http.MethodPost, ts.URL+applyAllURL,
		map[string]any{"presetId": "launch-bold", "overwrite": true})
	if status != http.StatusOK || payload["applied"] != float64(1) {
		t.Fatalf("overwrite 应覆盖人工页: %d %v", status, payload)
	}
	if r := applyAllResultOf(t, payload)["slide-1"]; r.Status != "applied" {
		t.Fatalf("overwrite 结果不对: %+v", r)
	}
}

func TestApplyAllIsolatesPageFailures(t *testing.T) {
	ts, _ := newApplyAllServer(t, []string{"slide-1", "slide-2", "slide-3"},
		map[string]bool{"slide-2": true})

	status, payload := doJSON(t, http.MethodPost, ts.URL+applyAllURL, map[string]any{"presetId": "business-calm"})
	if status != http.StatusOK {
		t.Fatalf("个别页失败不该整单 500: %d %v", status, payload)
	}
	if payload["applied"] != float64(2) || payload["skipped"] != float64(1) {
		t.Fatalf("汇总不对: %v", payload)
	}
	results := applyAllResultOf(t, payload)
	if results["slide-2"].Reason != "duration_unknown" {
		t.Fatalf("未配音页原因不对: %+v", results["slide-2"])
	}
	if results["slide-1"].Status != "applied" || results["slide-3"].Status != "applied" {
		t.Fatalf("其余页不该被牵连: %+v", results)
	}
}

func TestApplyAllRejectsUnknownPreset(t *testing.T) {
	ts, _ := newApplyAllServer(t, []string{"slide-1"}, nil)
	status, payload := doJSON(t, http.MethodPost, ts.URL+applyAllURL, map[string]any{"presetId": "not-a-preset"})
	if status != http.StatusBadRequest || payload["code"] != "invalid_preset" {
		t.Fatalf("未知预设应 400: %d %v", status, payload)
	}
}

func TestApplyAllDisabledReturnsNotFound(t *testing.T) {
	store := newAnimationTestStore(t)
	mux := http.NewServeMux()
	auth := func(h http.Handler) http.Handler {
		return AuthMiddlewareWithOptions(h, AuthOptions{
			LocalPrincipal: &Principal{TenantID: db.LocalTenantID, UserID: db.LocalUserID},
		})
	}
	NewAnimationHandler(store, &applyAllTestResolver{}, false).
		WithDeck(&fakeDeckResolver{ids: []string{"slide-1"}}).
		Register(mux, auth)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	status, payload := doJSON(t, http.MethodPost, ts.URL+applyAllURL, map[string]any{"presetId": "business-calm"})
	if status != http.StatusNotFound {
		t.Fatalf("灰度未开应 404: %d %v", status, payload)
	}
}
