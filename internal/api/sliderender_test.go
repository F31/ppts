package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
)

// slideRenderJobStub 让 /slides/render 走到 StepResultRef 分支：有成功解析任务，
// 但 pages 步骤没有 success 引用——渲染器缺失时 publishPageManifest 会把该步骤标 failed。
type slideRenderJobStub struct{ *jobCreatorStub }

func (s *slideRenderJobStub) LatestSucceededJob(context.Context, string, string, string) (*pipeline.Job, error) {
	return &pipeline.Job{ID: "job-parse", Kind: pipeline.KindParse, State: pipeline.StateSucceeded}, nil
}

// 渲染器缺失时清单已落盘但步骤是 failed，StepResultRef 查不到；接口必须回退直读清单，
// 把 renderer="unavailable" 透出，否则前端只能显示「渲染图就绪后展示」这种误导文案。
func TestEditorSlideRenderFallbackReportsUnavailable(t *testing.T) {
	const projectID = "project-1"
	projects := &fakeProjectStore{projects: []*project.Project{{
		ID: projectID, TenantID: "tenant-1", OwnerUser: "user-1", Title: "渲染器缺失", CurrentRevision: 2,
	}}}
	cases := []struct {
		name      string
		urlSuffix string
		manifest  bool
		want      string
	}{
		{"默认版本回退读清单", "", true, "unavailable"},
		{"显式 revision_no 回退读清单", "?revision_no=2", true, "unavailable"},
		{"清单不存在时不冒报", "", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objects := testObjects(t)
			if tc.manifest {
				key := objectstore.ObjectKey{
					TenantID: "tenant-1", ProjectID: projectID, Revision: "src-02",
					AssetType: "render", AssetID: "pages", Ext: "json",
				}
				putAPIObject(t, objects, key, []byte(`{"revisionNo":2,"renderer":"unavailable","pages":[]}`), "application/json")
			}
			jobs := &slideRenderJobStub{jobCreatorStub: &jobCreatorStub{}}
			server := httptest.NewServer(NewHandler(
				projects, newFakeUploadStore(), &fakeScriptStore{}, jobs,
				&fakeArtifactStore{}, objects, nil, Options{DevHeaders: true},
			))
			t.Cleanup(server.Close)

			req, err := http.NewRequest(http.MethodGet, server.URL+"/projects/"+projectID+"/slides/render"+tc.urlSuffix, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set(tenantHeader, "tenant-1")
			req.Header.Set(userHeader, "user-1")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			var body struct {
				Slides   []editorSlideRenderItem `json:"slides"`
				Renderer string                  `json:"renderer"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Renderer != tc.want {
				t.Fatalf("renderer = %q, want %q", body.Renderer, tc.want)
			}
			if len(body.Slides) != 0 {
				t.Fatalf("slides = %d, want 0", len(body.Slides))
			}
		})
	}
}

// 任务保留会删掉终态 parse 任务和成功步骤引用，页面 PNG 与 pages.json 仍在对象存储。
// 预览必须按约定键签发，否则前端只能显示「当前页面还没有渲染图」。
func TestEditorSlideRenderSignsStoredPagesWithoutJobStep(t *testing.T) {
	const projectID = "project-1"
	projects := &fakeProjectStore{projects: []*project.Project{{
		ID: projectID, TenantID: "tenant-1", OwnerUser: "user-1", Title: "已渲染", CurrentRevision: 1,
	}}}
	pageKey := objectstore.ObjectKey{
		TenantID: "tenant-1", ProjectID: projectID, Revision: "src-01",
		AssetType: "render", AssetID: "page-0001", Ext: "png",
	}
	manifest := []byte(`{"revisionNo":1,"renderer":"LibreOffice","pages":[{"slideId":"slide-1","index":0,"key":"` + pageKey.String() + `"}]}`)
	cases := []struct {
		name string
		jobs JobStore
	}{
		{"解析任务已被保留清理", &jobCreatorStub{}},
		{"成功步骤引用缺失", &slideRenderJobStub{jobCreatorStub: &jobCreatorStub{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objects := testObjects(t)
			putAPIObject(t, objects, pageKey, []byte("png"), "image/png")
			putAPIObject(t, objects, objectstore.ObjectKey{
				TenantID: "tenant-1", ProjectID: projectID, Revision: "src-01",
				AssetType: "render", AssetID: "pages", Ext: "json",
			}, manifest, "application/json")
			server := httptest.NewServer(NewHandler(
				projects, newFakeUploadStore(), &fakeScriptStore{}, tc.jobs,
				&fakeArtifactStore{}, objects, nil, Options{DevHeaders: true},
			))
			t.Cleanup(server.Close)

			req, err := http.NewRequest(http.MethodGet, server.URL+"/projects/"+projectID+"/slides/render", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set(tenantHeader, "tenant-1")
			req.Header.Set(userHeader, "user-1")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			var body struct {
				Slides   []editorSlideRenderItem `json:"slides"`
				Renderer string                  `json:"renderer"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Renderer != "LibreOffice" {
				t.Fatalf("renderer = %q, want LibreOffice", body.Renderer)
			}
			if len(body.Slides) != 1 || body.Slides[0].SlideID != "slide-1" {
				t.Fatalf("slides = %+v, want slide-1", body.Slides)
			}
			if !strings.Contains(body.Slides[0].URL, "/ppts/object/"+pageKey.String()) || !strings.Contains(body.Slides[0].URL, "token=") {
				t.Fatalf("url = %q, want signed object path", body.Slides[0].URL)
			}
		})
	}
}
