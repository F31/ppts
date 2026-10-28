package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/artifact"
)

// TestBundleExportRouteCreatesJob 守护 M6-1 的 REST 导出入口：
// 路由确实挂载（否则请求会落到 SPA 兜底返回 HTML），且 snapshot 与 Connect 入口同构。
func TestBundleExportRouteCreatesJob(t *testing.T) {
	jobs := &jobCreatorStub{}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, jobs, &fakeArtifactStore{}, testObjects(t), nil, Options{DevHeaders: true}))
	t.Cleanup(server.Close)

	body := `{"format":"html_bundle","timelineKey":"tenant-1/project-1/narration/timeline/tl.json","pagePngKeys":["tenant-1/project-1/render/page/p1.png"]}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/projects/project-1/exports", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(tenantHeader, "tenant-1")
	req.Header.Set(userHeader, "user-1")
	req.Header.Set("Idempotency-Key", "bundle-export-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/projects/project-1/exports: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("content-type = %q（说明命中了 SPA 兜底而不是 REST 端点）", resp.Header.Get("Content-Type"))
	}
	var out struct {
		JobID string `json:"jobId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.JobID == "" {
		t.Fatal("response has no jobId")
	}
	if jobs.tenantID != "tenant-1" || jobs.projectID != "project-1" {
		t.Fatalf("job scope = %s/%s", jobs.tenantID, jobs.projectID)
	}
	var snapshot app.ExportSnapshot
	if err := json.Unmarshal([]byte(jobs.inputSnapshot), &snapshot); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.Format != artifact.FormatHTMLBundle {
		t.Fatalf("snapshot format = %q, want html_bundle", snapshot.Format)
	}
	if snapshot.Width != 1920 || snapshot.Height != 1080 || snapshot.FPS != 30 {
		t.Fatalf("snapshot defaults = %dx%d@%d", snapshot.Width, snapshot.Height, snapshot.FPS)
	}
	if len(snapshot.PagePNGKeys) != 1 {
		t.Fatalf("page keys = %v", snapshot.PagePNGKeys)
	}
}

func TestBundleExportRouteValidation(t *testing.T) {
	jobs := &jobCreatorStub{}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, jobs, &fakeArtifactStore{}, testObjects(t), nil, Options{DevHeaders: true}))
	t.Cleanup(server.Close)

	cases := []struct {
		name       string
		body       string
		idempotent bool
		wantStatus int
	}{
		{
			name:       "missing idempotency key",
			body:       `{"format":"html_bundle","timelineKey":"tenant-1/project-1/narration/timeline/tl.json"}`,
			idempotent: false,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "timeline belongs to another project",
			body:       `{"format":"html_bundle","timelineKey":"tenant-1/project-2/narration/timeline/tl.json"}`,
			idempotent: true,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "another tenant timeline",
			body:       `{"format":"html_bundle","timelineKey":"tenant-2/project-1/narration/timeline/tl.json"}`,
			idempotent: true,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "unsupported format on this endpoint",
			body:       `{"format":"pptx","timelineKey":"tenant-1/project-1/narration/timeline/tl.json"}`,
			idempotent: true,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "mp4 without page keys",
			body:       `{"format":"mp4","timelineKey":"tenant-1/project-1/narration/timeline/tl.json"}`,
			idempotent: true,
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, server.URL+"/api/projects/project-1/exports", strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set(tenantHeader, "tenant-1")
			req.Header.Set(userHeader, "user-1")
			if tc.idempotent {
				req.Header.Set("Idempotency-Key", "bundle-export-"+strings.ReplaceAll(tc.name, " ", "-"))
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
		})
	}
}
