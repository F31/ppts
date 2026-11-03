package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/tenant"
)

// 文本复制开关（ADR-029 ⑥）在入口层的两条判据：
//  1. 开启时写进快照——它进 snapshotHash，写晚一步等于同一份输入能产出两种成品；
//  2. 只对 HTML 形态写——mp4 的画面是烧进像素的，写进去只会把同一份 mp4 拆成两个去重键。
func TestBundleExportCarriesTextCopyOnlyForHTML(t *testing.T) {
	yes := true
	policy := &tenant.Policy{Animation: &animation.AnimationPolicy{Enabled: &yes, TextCopy: &yes}}

	cases := []struct {
		format string
		body   string
		want   bool
	}{
		{format: "html_single", body: `{"format":"html_single","timelineKey":"tenant-1/project-1/narration/timeline/tl.json","pagePngKeys":["tenant-1/project-1/render/page/p1.png"]}`, want: true},
		{format: "html_bundle", body: `{"format":"html_bundle","timelineKey":"tenant-1/project-1/narration/timeline/tl.json","pagePngKeys":["tenant-1/project-1/render/page/p1.png"]}`, want: true},
		{format: "mp4", body: `{"format":"mp4","timelineKey":"tenant-1/project-1/narration/timeline/tl.json","pagePngKeys":["tenant-1/project-1/render/page/p1.png"]}`, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.format, func(t *testing.T) {
			jobs := &jobCreatorStub{}
			server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, jobs,
				&fakeArtifactStore{}, testObjects(t), nil, Options{
					DevHeaders: true, Policy: fakePolicyReader{policy: policy}, AnimationEnabled: true,
				}))
			t.Cleanup(server.Close)

			req, err := http.NewRequest(http.MethodPost, server.URL+"/api/projects/project-1/exports", strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set(tenantHeader, "tenant-1")
			req.Header.Set(userHeader, "user-1")
			req.Header.Set("Idempotency-Key", "textcopy-"+tc.format)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("status = %d, want 201", resp.StatusCode)
			}
			var snapshot app.ExportSnapshot
			if err := json.Unmarshal([]byte(jobs.inputSnapshot), &snapshot); err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			if snapshot.TextCopy != tc.want {
				t.Fatalf("%s: snapshot.textCopy = %v, want %v", tc.format, snapshot.TextCopy, tc.want)
			}
		})
	}
}

// 未配置策略时默认关闭：这条守的是「默认不许把讲稿原文带走」。
func TestBundleExportTextCopyDefaultsOff(t *testing.T) {
	jobs := &jobCreatorStub{}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, jobs,
		&fakeArtifactStore{}, testObjects(t), nil, Options{DevHeaders: true}))
	t.Cleanup(server.Close)

	body := `{"format":"html_single","timelineKey":"tenant-1/project-1/narration/timeline/tl.json","pagePngKeys":["tenant-1/project-1/render/page/p1.png"]}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/projects/project-1/exports", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(tenantHeader, "tenant-1")
	req.Header.Set(userHeader, "user-1")
	req.Header.Set("Idempotency-Key", "textcopy-default")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	// 关闭时字段必须整个不出现（omitempty）：存量快照的 hash 不能因为新增字段而变。
	if strings.Contains(jobs.inputSnapshot, "textCopy") {
		t.Fatalf("关闭时不应写出 textCopy：%s", jobs.inputSnapshot)
	}
}
