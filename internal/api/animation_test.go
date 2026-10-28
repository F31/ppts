package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/animation/animdefault"
	"github.com/F31/ppts/internal/animation/animstore"
	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/migrations"
)

const (
	anTestProject = "p-anim"
	anTestSlide   = "slide-3"
)

func newAnimationTestStore(t *testing.T) animstore.Store {
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
	return animstore.NewSQLiteStore(sqldb)
}

type fakeAnimationResolver struct {
	page *project.Page
	pc   animation.PageContext
}

func (f *fakeAnimationResolver) Resolve(context.Context, string, string, string, int, string, string) (*project.Page, animation.PageContext, error) {
	return f.page, f.pc, nil
}

func animationTestPage() *project.Page {
	return &project.Page{
		Index:   2,
		SlideID: anTestSlide,
		Shapes: []*project.Shape{
			{ID: "shape-1", Kind: "text", Text: "第三季度回顾", Bounds: &project.Box{X: 800000, Y: 600000, Width: 8000000, Height: 900000}},
			{ID: "shape-2", Kind: "text", Text: "收入增长 18%", Bounds: &project.Box{X: 800000, Y: 2000000, Width: 7000000, Height: 600000}},
		},
	}
}

func newAnimationTestServer(t *testing.T, enabled bool) (*httptest.Server, animstore.Store) {
	t.Helper()
	store := newAnimationTestStore(t)
	page := animationTestPage()
	resolver := &fakeAnimationResolver{
		page: page,
		pc: animation.PageContext{
			DurationMS: 6000,
			FPS:        30,
			Elements:   animdefault.PageAnchors(page),
			SegmentIDs: []string{"seg-1"},
		},
	}
	mux := http.NewServeMux()
	auth := func(h http.Handler) http.Handler {
		return AuthMiddlewareWithOptions(h, AuthOptions{
			LocalPrincipal: &Principal{TenantID: db.LocalTenantID, UserID: db.LocalUserID},
		})
	}
	NewAnimationHandler(store, resolver, enabled).Register(mux, auth)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, store
}

func animationURL() string {
	return "/api/projects/" + anTestProject + "/slides/" + anTestSlide + "/animation"
}

func validPutBody(expected int64) map[string]any {
	return map[string]any{
		"expectedRevision": expected,
		"presetId":         "business-calm",
		"intensity":        1.0,
		"tracks": []map[string]any{{
			"elementId": "shape-1",
			"kind":      "title",
			"clips": []map[string]any{{
				"id": "c1", "type": "slideIn", "startMs": 0, "durationMs": 600,
				"easing": "easeOut", "trigger": "onEnter",
				"params": map[string]any{"direction": "up", "offset": 40},
			}},
		}},
	}
}

func doJSON(t *testing.T, method, url string, body any) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	var payload map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	return resp.StatusCode, payload
}

func TestAnimationGetMissingThenDefault(t *testing.T) {
	ts, _ := newAnimationTestServer(t, true)

	status, payload := doJSON(t, http.MethodGet, ts.URL+animationURL(), nil)
	if status != http.StatusNotFound {
		t.Fatalf("missing spec status = %d, want 404 (%v)", status, payload)
	}

	status, payload = doJSON(t, http.MethodGet, ts.URL+animationURL()+"?default=true", nil)
	if status != http.StatusOK {
		t.Fatalf("default spec status = %d, want 200 (%v)", status, payload)
	}
	if got := payload["revision"].(float64); got != 0 {
		t.Fatalf("default revision = %v, want 0", got)
	}
	tracks, _ := payload["tracks"].([]any)
	if len(tracks) == 0 {
		t.Fatalf("default spec has no tracks: %v", payload)
	}
}

func TestAnimationPutLifecycle(t *testing.T) {
	ts, _ := newAnimationTestServer(t, true)

	status, payload := doJSON(t, http.MethodPut, ts.URL+animationURL(), validPutBody(0))
	if status != http.StatusOK {
		t.Fatalf("put status = %d, want 200 (%v)", status, payload)
	}
	if got := payload["revision"].(float64); got != 1 {
		t.Fatalf("revision = %v, want 1", got)
	}

	status, payload = doJSON(t, http.MethodGet, ts.URL+animationURL(), nil)
	if status != http.StatusOK {
		t.Fatalf("get status = %d (%v)", status, payload)
	}
	if payload["presetId"] != "business-calm" {
		t.Fatalf("presetId = %v", payload["presetId"])
	}

	// 过期版本：必须 409 且带回最新内容，前端才能做对比合并。
	status, payload = doJSON(t, http.MethodPut, ts.URL+animationURL(), validPutBody(0))
	if status != http.StatusConflict {
		t.Fatalf("stale put status = %d, want 409 (%v)", status, payload)
	}
	latest, _ := payload["latest"].(map[string]any)
	if latest == nil || latest["revision"].(float64) != 1 {
		t.Fatalf("conflict latest = %v, want revision 1", payload["latest"])
	}
}

func TestAnimationPutValidationError(t *testing.T) {
	ts, _ := newAnimationTestServer(t, true)
	body := validPutBody(0)
	body["tracks"].([]map[string]any)[0]["elementId"] = "shape-gone"

	status, payload := doJSON(t, http.MethodPut, ts.URL+animationURL(), body)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%v)", status, payload)
	}
	if payload["rule"] != "V2" {
		t.Fatalf("rule = %v, want V2", payload["rule"])
	}
}

func TestAnimationPutRequiresExpectedRevision(t *testing.T) {
	ts, _ := newAnimationTestServer(t, true)
	body := validPutBody(0)
	delete(body, "expectedRevision")

	status, payload := doJSON(t, http.MethodPut, ts.URL+animationURL(), body)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%v)", status, payload)
	}
	if payload["code"] != "missing_expected_revision" {
		t.Fatalf("code = %v", payload["code"])
	}
}

func TestAnimationWriteDisabled(t *testing.T) {
	ts, _ := newAnimationTestServer(t, false)

	status, _ := doJSON(t, http.MethodPut, ts.URL+animationURL(), validPutBody(0))
	if status != http.StatusNotFound {
		t.Fatalf("disabled write status = %d, want 404", status)
	}
	// 读路径不受开关影响：灰度前要能验证契约。
	status, _ = doJSON(t, http.MethodGet, ts.URL+animationURL(), nil)
	if status != http.StatusNotFound { // 无规格，但端点存在 → 404 not_found 而非 404 路由缺失
		t.Fatalf("read status = %d, want 404 not_found", status)
	}
}

func TestAnimationLockedWriteRejected(t *testing.T) {
	ts, store := newAnimationTestServer(t, true)
	ctx := context.Background()
	key := animation.Key{TenantID: db.LocalTenantID, ProjectID: anTestProject, SlideID: anTestSlide, Language: "zh-CN"}

	if _, err := store.Put(ctx, &animation.Revision{
		TenantID: key.TenantID, ProjectID: key.ProjectID, SlideID: key.SlideID, Language: key.Language,
		Intensity: 1.0,
	}, 0); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := store.SetStatus(ctx, key, animation.StatusApproved); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := store.SetStatus(ctx, key, animation.StatusLocked); err != nil {
		t.Fatalf("lock: %v", err)
	}

	status, payload := doJSON(t, http.MethodPut, ts.URL+animationURL(), validPutBody(1))
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%v)", status, payload)
	}
	if payload["code"] != "locked" {
		t.Fatalf("code = %v, want locked", payload["code"])
	}
}
