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

// TestBundleExportDropsAnimationWhenUnavailable 守「快照说动画、产物却是静态」这条裂缝。
//
// 事故形态：部署关掉动画后，客户端仍带着 specs 提交导出。此前入口不看开关，
// 快照照旧带 animation，worker 却只能静态编码——同一个 snapshot_hash 被写成静态字节，
// 覆盖掉上一次那份动画产物。用户再下载"动画版"拿到静态版，全程无报错。
// 现在的要求：入口自己判一次能力，渲不出来就把动画从快照里摘掉，并把原因回传。
func TestBundleExportDropsAnimationWhenUnavailable(t *testing.T) {
	body := func() string {
		return `{"format":"mp4","timelineKey":"tenant-1/project-1/narration/timeline/tl.json",` +
			`"pagePngKeys":["tenant-1/project-1/render/page/p1.png"],` +
			`"animation":{"specs":[{"slideId":"slide-1","language":"zh-CN","sourceRevisionNo":1,"revision":2}]}}`
	}
	cases := []struct {
		name        string
		enabled     bool
		probe       *stubProbe
		wantDropped string
	}{
		{name: "部署关闭动画", enabled: false, probe: &stubProbe{}, wantDropped: "animation_disabled_by_deployment"},
		{name: "渲染服务不可达", enabled: true, probe: &stubProbe{err: errUnreachable}, wantDropped: "renderer_unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			jobs := &jobCreatorStub{}
			opts := Options{DevHeaders: true, Animation: newAnimationTestStore(t),
				AnimationEnabled: tc.enabled, AnimationProbe: tc.probe}
			server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{},
				jobs, &fakeArtifactStore{}, testObjects(t), nil, opts))
			t.Cleanup(server.Close)

			req, err := http.NewRequest(http.MethodPost, server.URL+"/api/projects/project-1/exports", strings.NewReader(body()))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set(tenantHeader, "tenant-1")
			req.Header.Set(userHeader, "user-1")
			req.Header.Set("Idempotency-Key", "drop-animation-1")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("status = %d, want 201", resp.StatusCode)
			}
			var out struct {
				JobID            string `json:"jobId"`
				AnimationDropped string `json:"animationDropped"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !strings.HasPrefix(out.AnimationDropped, tc.wantDropped) {
				t.Fatalf("animationDropped = %q, want prefix %q", out.AnimationDropped, tc.wantDropped)
			}
			var snapshot app.ExportSnapshot
			if err := json.Unmarshal([]byte(jobs.inputSnapshot), &snapshot); err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			if snapshot.Animation != nil {
				t.Fatal("降级后快照里不能再留着 animation：同键会被写成静态字节，覆盖上一次的动画产物")
			}
		})
	}
}

// TestBundleExportKeepsAnimationWhenAvailable 是上面那条的反面：
// 能力齐备时不许"顺手"把动画摘掉，否则降级逻辑会把正常路径一起吞了。
func TestBundleExportKeepsAnimationWhenAvailable(t *testing.T) {
	jobs := &jobCreatorStub{}
	opts := Options{DevHeaders: true, Animation: newAnimationTestStore(t),
		AnimationEnabled: true, AnimationProbe: &stubProbe{}}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{},
		jobs, &fakeArtifactStore{}, testObjects(t), nil, opts))
	t.Cleanup(server.Close)

	body := `{"format":"mp4","timelineKey":"tenant-1/project-1/narration/timeline/tl.json",` +
		`"pagePngKeys":["tenant-1/project-1/render/page/p1.png"],` +
		`"animation":{"specs":[{"slideId":"slide-1","language":"zh-CN","sourceRevisionNo":1,"revision":2}]}}`
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/projects/project-1/exports", strings.NewReader(body))
	req.Header.Set(tenantHeader, "tenant-1")
	req.Header.Set(userHeader, "user-1")
	req.Header.Set("Idempotency-Key", "keep-animation-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	var out struct {
		AnimationDropped string `json:"animationDropped"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.AnimationDropped != "" {
		t.Fatalf("能力齐备却降级了: %q", out.AnimationDropped)
	}
	var snapshot app.ExportSnapshot
	if err := json.Unmarshal([]byte(jobs.inputSnapshot), &snapshot); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.Animation == nil || len(snapshot.Animation.Specs) != 1 {
		t.Fatalf("快照里应当保留动画规格，got %+v", snapshot.Animation)
	}
}
