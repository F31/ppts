package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/project"
)

// G2-2 并排对照端点的 HTTP 契约。
//
// 守的是"对照面板看到的原文 = 成稿实际读到的素材"，以及缺稿/缺解析产物时
// 的失败语义（界面要能区分"这页还没生成讲稿"和"项目还没解析完"）。

const (
	compareProject = "project-compare"
	compareSlide   = "slide-3"
	compareTenant  = db.LocalTenantID
)

type compareObjectStore struct {
	objects map[string][]byte
	gets    int
}

func (s *compareObjectStore) Put(_ context.Context, key objectstore.ObjectKey, r io.Reader, _ objectstore.ObjectMeta) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.objects[key.String()] = data
	return nil
}

func (s *compareObjectStore) Get(_ context.Context, key objectstore.ObjectKey) (io.ReadCloser, objectstore.ObjectMeta, error) {
	s.gets++
	data, ok := s.objects[key.String()]
	if !ok {
		return nil, objectstore.ObjectMeta{}, objectstore.ErrObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), objectstore.ObjectMeta{Size: int64(len(data))}, nil
}

func (s *compareObjectStore) SignedURL(context.Context, objectstore.ObjectKey, objectstore.Operation, time.Duration) (string, error) {
	return "", objectstore.ErrOperationNotSupported
}

func (s *compareObjectStore) Delete(context.Context, objectstore.ObjectKey) error { return nil }

func (s *compareObjectStore) ApplyLifecyclePolicy(context.Context, string, objectstore.LifecyclePolicy) error {
	return objectstore.ErrOperationNotSupported
}

type compareSourceStore map[string]app.ScriptSourceChoice

func (compareSourceStore) Set(context.Context, string, string, int, string, app.ScriptSourceKind, string) error {
	return nil
}

func (s compareSourceStore) List(context.Context, string, string, int) (map[string]app.ScriptSourceChoice, error) {
	return map[string]app.ScriptSourceChoice(s), nil
}

func compareDocKey(revision string) string {
	return objectstore.ObjectKey{
		TenantID: compareTenant, ProjectID: compareProject,
		Revision: revision, AssetType: "document", AssetID: "extracted", Ext: "json",
	}.String()
}

func compareDocumentJSON(t *testing.T, pages ...*project.Page) []byte {
	t.Helper()
	raw, err := json.Marshal(project.Document{Pages: pages})
	if err != nil {
		t.Fatalf("marshal document: %v", err)
	}
	return raw
}

func newCompareServer(t *testing.T, objects *compareObjectStore, scripts narration.Store, sources app.ScriptSourceStore, currentRevision int) *httptest.Server {
	t.Helper()
	projects := &fakeProjectStore{projects: []*project.Project{{
		ID: compareProject, TenantID: compareTenant, OwnerUser: db.LocalUserID,
		Title: "对照项目", CurrentRevision: currentRevision,
	}}}
	mux := http.NewServeMux()
	auth := func(h http.Handler) http.Handler {
		return AuthMiddlewareWithOptions(h, AuthOptions{
			LocalPrincipal: &Principal{TenantID: compareTenant, UserID: db.LocalUserID},
		})
	}
	registerEditorRoutes(mux, &editorDeps{objects: objects, scripts: scripts, scriptSources: sources, projects: projects, auth: auth})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func compareURL() string {
	return "/projects/" + compareProject + "/slides/" + compareSlide + "/script/compare"
}

func compareRevision() *narration.Revision {
	return &narration.Revision{
		ID: "rev-1", TenantID: compareTenant, ProjectID: compareProject, SlideID: compareSlide,
		Language: "zh-CN", Mode: narration.ModePolish, Revision: 7, SourceRevisionNo: 2,
		Segments: []*narration.Segment{
			{SegmentID: "seg-01", DisplayText: "这一页我们看三季度的增长。", Status: narration.StatusDraft},
			{SegmentID: "seg-02", DisplayText: "营收同比增长百分之二十三。", Status: narration.StatusDraft},
		},
	}
}

func TestScriptCompareReturnsOriginalAndCurrent(t *testing.T) {
	objects := &compareObjectStore{objects: map[string][]byte{
		compareDocKey("src-02"): compareDocumentJSON(t, &project.Page{
			Index: 2, SlideID: compareSlide, Name: "第三季度",
			NotesText: "备注里写的原话",
			Shapes: []*project.Shape{
				{ID: "s1", Kind: "title", Text: "第三季度回顾"},
				{ID: "s2", Kind: "body", Text: "营收同比增长 23%"},
			},
		}),
	}}
	ts := newCompareServer(t, objects, &fakeScriptStore{revision: compareRevision()}, nil, 2)

	status, payload := doJSON(t, http.MethodGet, ts.URL+compareURL(), nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%v)", status, payload)
	}
	// 原文按版本隔离取 src-02，且默认来源是版面文字（不是备注）：
	// 这与成稿 worker 的默认输入一致，用户看到的"原文"才是模型当时吃的那段。
	if got := payload["originalText"]; got != "第三季度回顾\n营收同比增长 23%" {
		t.Fatalf("originalText = %q", got)
	}
	if got := payload["source"]; got != "page" {
		t.Fatalf("source = %v, want page", got)
	}
	if got := payload["mode"]; got != string(narration.ModePolish) {
		t.Fatalf("mode = %v", got)
	}
	if got := payload["revisionNo"].(float64); got != 2 {
		t.Fatalf("revisionNo = %v, want 2（项目当前版本）", got)
	}
	if got := payload["scriptRevision"].(float64); got != 7 {
		t.Fatalf("scriptRevision = %v, want 7", got)
	}
	// 多段讲稿按换行拼接：对照面板按句切分，段边界必须保留下来。
	if got := payload["currentText"]; got != "这一页我们看三季度的增长。\n营收同比增长百分之二十三。" {
		t.Fatalf("currentText = %q", got)
	}
	segments, _ := payload["segments"].([]any)
	if len(segments) != 2 || segments[1].(map[string]any)["segmentId"] != "seg-02" {
		t.Fatalf("segments = %v", segments)
	}
}

func TestScriptCompareUsesStoredSourceChoice(t *testing.T) {
	objects := &compareObjectStore{objects: map[string][]byte{
		compareDocKey("src-02"): compareDocumentJSON(t, &project.Page{
			Index: 2, SlideID: compareSlide,
			NotesText: "备注里写的原话",
			Shapes:    []*project.Shape{{ID: "s1", Kind: "body", Text: "版面文字"}},
		}),
	}}
	sources := compareSourceStore{compareSlide: {SlideID: compareSlide, Kind: app.ScriptSourceNotes}}
	ts := newCompareServer(t, objects, &fakeScriptStore{revision: compareRevision()}, sources, 2)

	status, payload := doJSON(t, http.MethodGet, ts.URL+compareURL(), nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d (%v)", status, payload)
	}
	// 页面级来源=备注时，对照必须跟着走备注：否则用户看到"原文"是版面，
	// 而讲稿其实是从备注生成的，两边根本不是一对。
	if got := payload["originalText"]; got != "备注里写的原话" {
		t.Fatalf("originalText = %q", got)
	}
	if got := payload["source"]; got != "notes" {
		t.Fatalf("source = %v, want notes", got)
	}
}

func TestScriptCompareHonorsSourceRevisionHeader(t *testing.T) {
	// 同一 slide_id 在两个源版本里内容不同：版本隔离错了就会拿旧版原文去对讲稿。
	objects := &compareObjectStore{objects: map[string][]byte{
		compareDocKey("src-01"): compareDocumentJSON(t, &project.Page{
			Index: 0, SlideID: compareSlide, Shapes: []*project.Shape{{ID: "s1", Kind: "body", Text: "旧版原文"}},
		}),
		compareDocKey("src-02"): compareDocumentJSON(t, &project.Page{
			Index: 0, SlideID: compareSlide, Shapes: []*project.Shape{{ID: "s1", Kind: "body", Text: "新版原文"}},
		}),
	}}
	ts := newCompareServer(t, objects, &fakeScriptStore{revision: compareRevision()}, nil, 2)

	req, err := http.NewRequest(http.MethodGet, ts.URL+compareURL(), nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set(sourceRevisionHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (%v)", resp.StatusCode, payload)
	}
	if got := payload["originalText"]; got != "旧版原文" {
		t.Fatalf("originalText = %q, want 旧版原文（按请求头版本读取）", got)
	}
	if got := payload["revisionNo"].(float64); got != 1 {
		t.Fatalf("revisionNo = %v, want 1", got)
	}
}

func TestScriptCompareMissingScriptIsDistinguishable(t *testing.T) {
	objects := &compareObjectStore{objects: map[string][]byte{
		compareDocKey("src-02"): compareDocumentJSON(t, &project.Page{Index: 0, SlideID: compareSlide}),
	}}
	ts := newCompareServer(t, objects, &fakeScriptStore{}, nil, 2)

	status, payload := doJSON(t, http.MethodGet, ts.URL+compareURL(), nil)
	if status != http.StatusNotFound || payload["code"] != "script_not_found" {
		t.Fatalf("status=%d payload=%v, want 404 script_not_found", status, payload)
	}
}

func TestScriptCompareMissingDocumentIsDistinguishable(t *testing.T) {
	objects := &compareObjectStore{objects: map[string][]byte{}}
	ts := newCompareServer(t, objects, &fakeScriptStore{revision: compareRevision()}, nil, 2)

	status, payload := doJSON(t, http.MethodGet, ts.URL+compareURL(), nil)
	// 不能退化成 500：界面要能说清"项目还没解析完"，而不是"服务出错"。
	if status != http.StatusNotFound || payload["code"] != "no_parsed_document" {
		t.Fatalf("status=%d payload=%v, want 404 no_parsed_document", status, payload)
	}
}

func TestScriptCompareEmptySourceBecomesNone(t *testing.T) {
	// 页面既无形状文字也无备注（纯图片页）：source=none 让界面显示"本页没有可对照的原文"，
	// 而不是给一个空白的"页面原文"标签。
	objects := &compareObjectStore{objects: map[string][]byte{
		compareDocKey("src-02"): compareDocumentJSON(t, &project.Page{Index: 0, SlideID: compareSlide}),
	}}
	ts := newCompareServer(t, objects, &fakeScriptStore{revision: compareRevision()}, nil, 2)

	status, payload := doJSON(t, http.MethodGet, ts.URL+compareURL(), nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d (%v)", status, payload)
	}
	if got := payload["source"]; got != "none" {
		t.Fatalf("source = %v, want none", got)
	}
	if got := payload["originalText"]; got != "" {
		t.Fatalf("originalText = %q, want 空", got)
	}
}
