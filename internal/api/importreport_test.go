package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/project"
)

// 导入报告的价值全在"诚实"上：解析没产出、产物坏了、版本不存在，三种情况
// 对用户意味着完全不同的下一步。这些用例逐条钉住状态码与 code，
// 防止它们被压成一个笼统的 500 / 空报告。

type reportProjectStore struct {
	*fakeProjectStore
	revision *project.SourceRevision
	err      error
}

func (s *reportProjectStore) GetSourceRevision(context.Context, string, string, int) (*project.SourceRevision, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.revision, nil
}

func importReportKey(rev string) objectstore.ObjectKey {
	return objectstore.ObjectKey{
		TenantID: "tenant-1", ProjectID: "project-1",
		Revision: rev, AssetType: "document", AssetID: "extracted", Ext: "json",
	}
}

func importReportServer(t *testing.T, store project.ProjectStore, objects objectstore.ObjectStore) *httptest.Server {
	t.Helper()
	h := NewHandler(store, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{}, &fakeArtifactStore{}, objects, nil,
		Options{Members: &fakeRoleReader{role: membership.RoleViewer}, DevHeaders: true})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func getImportReport(t *testing.T, srv *httptest.Server, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set(tenantHeader, "tenant-1")
	req.Header.Set(userHeader, "user-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestImportReportReturnsParsedFacts(t *testing.T) {
	store := &reportProjectStore{
		fakeProjectStore: &fakeProjectStore{projects: []*project.Project{{
			ID: "project-1", TenantID: "tenant-1", OwnerUser: "user-1", Title: "Demo",
		}}},
		revision: &project.SourceRevision{
			ID: "rev-1", ProjectID: "project-1", TenantID: "tenant-1", RevisionNo: 2,
			DisplayName: "季度汇报", PageCount: 9, ParserVersion: "go-pptx-v2.0.0",
			CreatedAt: time.Unix(1700000000, 0),
		},
	}
	objects := testObjects(t)
	putAPIObject(t, objects, importReportKey("src-02"), []byte(`{
		"schemaVersion":"1.0","parserName":"go-pptx","parserVersion":"v2.0.0",
		"pages":[
			{"index":0,"slideId":"slide-1","name":"封面","notesText":"开场","shapes":[{"id":"s1","kind":"textbox","text":"标题"}],"featureFlags":["picture"]},
			{"index":1,"slideId":"slide-2","name":"架构","hidden":true,"shapes":[{"id":"s2","kind":"audio"},{"id":"s3","kind":"smartart","opaque":true}],"featureFlags":["hidden","timing"],"hasTiming":true},
			{"index":2,"slideId":"slide-3","name":"数据","notesText":"看图","shapes":[{"id":"s4","kind":"table","text":"a\tb"}]}
		],
		"features":{"pageCount":3,"pictures":1,"audioClips":1,"tableShapes":1,"opaqueShapes":1,"slidesWithTiming":1,"pagesWithNotes":2}
	}`), "application/json")
	srv := importReportServer(t, store, objects)

	resp := getImportReport(t, srv, "/projects/project-1/revisions/2/import-report")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body = %v", resp.StatusCode, decodeErrorCode(t, resp))
	}
	var payload struct {
		Revision importReportRevision `json:"revision"`
		Report   struct {
			PageCount         int               `json:"pageCount"`
			HiddenPages       int               `json:"hiddenPages"`
			PagesWithNotes    int               `json:"pagesWithNotes"`
			PagesWithoutNotes int               `json:"pagesWithoutNotes"`
			ParserName        string            `json:"parserName"`
			Findings          []project.Finding `json:"findings"`
			Pages             []project.ReportPage
		} `json:"report"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Revision.RevisionNo != 2 || payload.Revision.DisplayName != "季度汇报" {
		t.Errorf("版本元信息 = %+v", payload.Revision)
	}
	// 回写页数（9）与解析产物（3）不一致时必须两个都给出：这是"回写没落上"的唯一线索。
	if payload.Revision.PageCount != 9 {
		t.Errorf("版本页数 = %d，期望保留回写值 9", payload.Revision.PageCount)
	}
	if payload.Report.PageCount != 3 || payload.Report.HiddenPages != 1 || payload.Report.PagesWithNotes != 2 || payload.Report.PagesWithoutNotes != 1 {
		t.Errorf("报告汇总 = %+v", payload.Report)
	}
	if payload.Report.ParserName != "go-pptx" {
		t.Errorf("解析器 = %q", payload.Report.ParserName)
	}
	if len(payload.Report.Pages) != 3 || payload.Report.Pages[1].ShapeCount != 2 || payload.Report.Pages[1].OpaqueCount != 1 {
		t.Fatalf("逐页摘要 = %+v", payload.Report.Pages)
	}
	codes := map[string]project.Finding{}
	for _, f := range payload.Report.Findings {
		codes[f.Code] = f
	}
	if f, ok := codes[project.FindingOpaqueShapes]; !ok || f.Count != 1 {
		t.Errorf("缺少不透明形状条目：%+v", payload.Report.Findings)
	}
	if f, ok := codes[project.FindingHiddenPages]; !ok || len(f.Pages) != 1 || f.Pages[0] != 1 {
		t.Errorf("隐藏页条目 = %+v", codes[project.FindingHiddenPages])
	}
	if _, ok := codes[project.FindingParseDiagnostics]; ok {
		t.Errorf("无诊断时不该出现诊断条目：%+v", payload.Report.Findings)
	}
}

func TestImportReportNotReadyIsConflictNotNotFound(t *testing.T) {
	// 解析还在排队/进行中：版本是真的，产物还没写。返回 404 会让用户以为版本丢了，
	// 返回 200 + 空报告会让界面显示"0 页"——两种都错。
	store := &reportProjectStore{
		fakeProjectStore: &fakeProjectStore{projects: []*project.Project{{
			ID: "project-1", TenantID: "tenant-1", OwnerUser: "user-1", Title: "Demo",
		}}},
		revision: &project.SourceRevision{RevisionNo: 1, TenantID: "tenant-1", PageCount: 0},
	}
	srv := importReportServer(t, store, testObjects(t))

	resp := getImportReport(t, srv, "/projects/project-1/revisions/1/import-report")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d，期望 409（解析尚未产出）", resp.StatusCode)
	}
	if code := decodeErrorCode(t, resp)["code"]; code != "report_not_ready" {
		t.Errorf("code = %v，期望 report_not_ready", code)
	}
}

func TestImportReportRevisionNotFound(t *testing.T) {
	store := &reportProjectStore{
		fakeProjectStore: &fakeProjectStore{projects: []*project.Project{{
			ID: "project-1", TenantID: "tenant-1", OwnerUser: "user-1", Title: "Demo",
		}}},
		err: project.ErrSourceRevisionNotFound,
	}
	srv := importReportServer(t, store, testObjects(t))

	resp := getImportReport(t, srv, "/projects/project-1/revisions/7/import-report")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d，期望 404", resp.StatusCode)
	}
	if code := decodeErrorCode(t, resp)["code"]; code != "revision_not_found" {
		t.Errorf("code = %v，期望 revision_not_found", code)
	}
}

func TestImportReportCorruptDocumentIsNotRetryable(t *testing.T) {
	// 产物读不懂是坏了，不是"还没好"。若归成 not_ready，前端会一直提示"稍后再试"，
	// 用户永远等不到结果，也永远看不到真正的原因。
	store := &reportProjectStore{
		fakeProjectStore: &fakeProjectStore{projects: []*project.Project{{
			ID: "project-1", TenantID: "tenant-1", OwnerUser: "user-1", Title: "Demo",
		}}},
		revision: &project.SourceRevision{RevisionNo: 1, TenantID: "tenant-1", PageCount: 2},
	}
	objects := testObjects(t)
	putAPIObject(t, objects, importReportKey("src-01"), []byte("{not json"), "application/json")
	srv := importReportServer(t, store, objects)

	resp := getImportReport(t, srv, "/projects/project-1/revisions/1/import-report")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d，期望 500", resp.StatusCode)
	}
	if code := decodeErrorCode(t, resp)["code"]; code != "report_corrupt" {
		t.Errorf("code = %v，期望 report_corrupt", code)
	}
}

func TestImportReportRejectsInvalidRevisionNumber(t *testing.T) {
	store := &reportProjectStore{
		fakeProjectStore: &fakeProjectStore{projects: []*project.Project{{
			ID: "project-1", TenantID: "tenant-1", OwnerUser: "user-1", Title: "Demo",
		}}},
	}
	srv := importReportServer(t, store, testObjects(t))
	resp := getImportReport(t, srv, "/projects/project-1/revisions/0/import-report")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d，期望 400", resp.StatusCode)
	}
}
