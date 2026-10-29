package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/animation/animstore"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/media"
)

// 本文件守护「导出选项 API」的两件事：
//
//  1. 快照里的 animation 只能由**经过校验**的请求写入（否则用户会拿到一个跑完的静态视频，
//     然后发现"我明明勾了动画"）；
//  2. 能力判定必须说明**缺的是哪一段**（规格未上线 / 部署没开 / 渲染服务不可达），
//     因为前端置灰时唯一能给用户的信息就是这句原因。

func TestAnimationExportOptionsValidation(t *testing.T) {
	ok := func(n int64) *bundleAnimationRequest {
		return &bundleAnimationRequest{Specs: []bundleAnimationSpec{
			{SlideID: " slide-1 ", Language: " zh-CN ", SourceRevisionNo: 2, Revision: n},
		}}
	}
	cases := []struct {
		name    string
		format  string
		in      *bundleAnimationRequest
		wantErr string
	}{
		{name: "nil is static", format: "mp4", in: nil},
		{name: "empty specs is static", format: "mp4", in: &bundleAnimationRequest{}},
		{
			name: "animation on non-mp4 is rejected", format: "html_bundle", in: ok(1),
			wantErr: "only available for mp4",
		},
		{
			name:   "blank slide id",
			format: "mp4",
			in: &bundleAnimationRequest{Specs: []bundleAnimationSpec{
				{SlideID: "   ", Revision: 1},
			}},
			wantErr: "slideId is required",
		},
		{
			name:   "duplicate slide id",
			format: "mp4",
			in: &bundleAnimationRequest{Specs: []bundleAnimationSpec{
				{SlideID: "slide-1", Revision: 1},
				{SlideID: "slide-1", Revision: 2},
			}},
			wantErr: "duplicate slideId",
		},
		{
			name:   "revision zero",
			format: "mp4",
			in: &bundleAnimationRequest{Specs: []bundleAnimationSpec{
				{SlideID: "slide-1", Revision: 0},
			}},
			wantErr: "revision must be positive",
		},
		{
			name:   "negative source revision",
			format: "mp4",
			in: &bundleAnimationRequest{Specs: []bundleAnimationSpec{
				{SlideID: "slide-1", SourceRevisionNo: -1, Revision: 1},
			}},
			wantErr: "must not be negative",
		},
		{
			name:   "too many specs",
			format: "mp4",
			in: &bundleAnimationRequest{Specs: func() []bundleAnimationSpec {
				out := make([]bundleAnimationSpec, maxAnimationSpecsPerExport+1)
				for i := range out {
					out[i] = bundleAnimationSpec{SlideID: "slide-" + strconv.Itoa(i), Revision: 1}
				}
				return out
			}()},
			wantErr: "too many animation specs",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := animationExportOptions(tc.format, tc.in)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if got != nil {
					t.Fatalf("want nil options for static export, got %+v", got)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want contains %q", err, tc.wantErr)
			}
		})
	}
}

func TestAnimationExportOptionsNormalizesFields(t *testing.T) {
	got, err := animationExportOptions("mp4", &bundleAnimationRequest{Specs: []bundleAnimationSpec{
		{SlideID: " slide-1 ", Language: " zh-CN ", SourceRevisionNo: 3, Revision: 7},
		{SlideID: "slide-2", Revision: 1}, // language 空 → 留给 animation.Key.Normalize
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || len(got.Specs) != 2 {
		t.Fatalf("specs = %+v", got)
	}
	if got.Specs[0].SlideID != "slide-1" || got.Specs[0].Language != "zh-CN" {
		t.Fatalf("spec[0] not trimmed: %+v", got.Specs[0])
	}
	if got.Specs[1].SourceRevisionNo != 0 || got.Specs[1].Revision != 1 {
		t.Fatalf("spec[1] = %+v", got.Specs[1])
	}
}

// stubProbe 记录被探测的次数：能力判定里"关闭时不该探测"是个真实约束
// （每次探测都是一次网络往返，关闭态还去探等于给用户白等）。
type stubProbe struct {
	err   error
	calls int
}

func (s *stubProbe) Check(context.Context) error {
	s.calls++
	return s.err
}

func TestAnimationOptionsCapabilityReasons(t *testing.T) {
	cases := []struct {
		name        string
		withStore   bool
		enabled     bool
		probe       *stubProbe
		wantEnabled bool
		wantReason  string
		wantProbes  int
	}{
		{name: "no spec store", withStore: false, enabled: true, wantReason: "animation_not_available"},
		{name: "deployment switch off", withStore: true, enabled: false, probe: &stubProbe{}, wantReason: "animation_disabled_by_deployment"},
		{name: "no renderer configured", withStore: true, enabled: true, wantReason: "renderer_not_configured"},
		{
			name: "renderer unreachable", withStore: true, enabled: true,
			probe: &stubProbe{err: errUnreachable}, wantReason: "renderer_unavailable", wantProbes: 1,
		},
		{name: "all good", withStore: true, enabled: true, probe: &stubProbe{}, wantEnabled: true, wantProbes: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var store animstore.Store
			if tc.withStore {
				store = newAnimationTestStore(t)
			}
			opts := Options{DevHeaders: true}
			if store != nil {
				opts.Animation = store
			}
			opts.AnimationEnabled = tc.enabled
			if tc.probe != nil {
				opts.AnimationProbe = tc.probe
			}
			server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil, opts))
			t.Cleanup(server.Close)

			resp := getAnimationOptions(t, server.URL, "")
			if resp.Enabled != tc.wantEnabled {
				t.Fatalf("enabled = %v, want %v (reason %q)", resp.Enabled, tc.wantEnabled, resp.Reason)
			}
			if !strings.HasPrefix(resp.Reason, tc.wantReason) {
				t.Fatalf("reason = %q, want prefix %q", resp.Reason, tc.wantReason)
			}
			if tc.probe != nil && tc.probe.calls != tc.wantProbes {
				t.Fatalf("probe calls = %d, want %d", tc.probe.calls, tc.wantProbes)
			}
		})
	}
}

var errUnreachable = &netError{msg: "connection refused"}

type netError struct{ msg string }

func (e *netError) Error() string { return e.msg }

// TestAnimationOptionsPagesFollowTimeline 守护端点给出的页身份与时间轴一致：
// 前端把这份身份原样放进导出请求，错一页就等于把动画挂到别的页上。
func TestAnimationOptionsPagesFollowTimeline(t *testing.T) {
	store := newAnimationTestStore(t)
	ctx := context.Background()
	seed := func(slideID string, sourceRevisionNo int) *animation.Revision {
		rev, err := store.Put(ctx, &animation.Revision{
			TenantID: "tenant-1", ProjectID: "project-1", SlideID: slideID,
			Language: "zh-CN", SourceRevisionNo: sourceRevisionNo, Intensity: 1,
		}, 0)
		if err != nil {
			t.Fatalf("seed %s: %v", slideID, err)
		}
		return rev
	}
	// slide-2 故意不配规格：它必须出现在 pages 里且 revision=0，
	// 这样前端能显示"3 页里 2 页有动效"，而不是自己数。
	rev1 := seed("slide-1", 0)
	rev3 := seed("slide-3", 0)

	objects := testObjects(t)
	timeline, err := media.BuildTimeline([]media.SlideInput{
		{SlideID: "slide-1", Segments: []media.SegmentInput{{SegmentID: "seg-1", DisplayText: "一", AudioKey: "tenant-1/project-1/cache/audio/a.wav", DurationMS: 1000}}},
		{SlideID: "slide-2", Segments: []media.SegmentInput{{SegmentID: "seg-2", DisplayText: "二", AudioKey: "tenant-1/project-1/cache/audio/b.wav", DurationMS: 1000}}},
		{SlideID: "slide-3", Segments: []media.SegmentInput{{SegmentID: "seg-3", DisplayText: "三", AudioKey: "tenant-1/project-1/cache/audio/c.wav", DurationMS: 1000}}},
	}, media.Timing{})
	if err != nil {
		t.Fatal(err)
	}
	srtKey := objectstore.ObjectKey{TenantID: "tenant-1", ProjectID: "project-1", Revision: "narration", AssetType: "subtitle", AssetID: "sub", Ext: "srt"}
	vttKey := objectstore.ObjectKey{TenantID: "tenant-1", ProjectID: "project-1", Revision: "narration", AssetType: "subtitle", AssetID: "sub", Ext: "vtt"}
	timelineKey := objectstore.ObjectKey{TenantID: "tenant-1", ProjectID: "project-1", Revision: "narration", AssetType: "timeline", AssetID: "tl", Ext: "json"}
	bundle, _ := json.Marshal(app.TimelineAsset{Timeline: timeline, SRTKey: srtKey.String(), VTTKey: vttKey.String()})
	putAPIObject(t, objects, timelineKey, bundle, "application/json")

	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{}, &fakeArtifactStore{}, objects, nil, Options{
		DevHeaders: true, Animation: store, AnimationEnabled: true, AnimationProbe: &stubProbe{},
	}))
	t.Cleanup(server.Close)

	resp := getAnimationOptions(t, server.URL, timelineKey.String())
	if !resp.Enabled || !resp.HasTimeline {
		t.Fatalf("enabled=%v hasTimeline=%v reason=%q", resp.Enabled, resp.HasTimeline, resp.Reason)
	}
	if len(resp.Pages) != 3 {
		t.Fatalf("pages = %+v", resp.Pages)
	}
	want := []struct {
		slideID  string
		revision int64
	}{{"slide-1", rev1.Revision}, {"slide-2", 0}, {"slide-3", rev3.Revision}}
	for i, w := range want {
		got := resp.Pages[i]
		if got.SlideID != w.slideID || got.Revision != w.revision {
			t.Fatalf("page[%d] = %+v, want %+v", i, got, w)
		}
		if got.Language != "zh-CN" {
			t.Fatalf("page[%d].language = %q（空语言必须给出默认值，否则前端拼的 key 与后端 Normalize 结果不一致）", i, got.Language)
		}
	}
}

// TestAnimationOptionsRejectsCrossProjectTimeline：时间轴归属校验与导出入口同一规则。
func TestAnimationOptionsRejectsCrossProjectTimeline(t *testing.T) {
	store := newAnimationTestStore(t)
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil, Options{
		DevHeaders: true, Animation: store, AnimationEnabled: true, AnimationProbe: &stubProbe{},
	}))
	t.Cleanup(server.Close)

	req, _ := http.NewRequest(http.MethodGet,
		server.URL+"/api/projects/project-1/exports/animation-options?timelineKey=tenant-1%2Fproject-2%2Fnarration%2Ftimeline%2Ftl.json", nil)
	req.Header.Set(tenantHeader, "tenant-1")
	req.Header.Set(userHeader, "user-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400（跨项目时间轴必须拒绝）", resp.StatusCode)
	}
}

// TestBundleExportCarriesAnimationIntoSnapshot：请求里的 specs 必须真的进快照，
// 否则"勾了动画"只停留在 UI 上。
func TestBundleExportCarriesAnimationIntoSnapshot(t *testing.T) {
	jobs := &jobCreatorStub{}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, jobs, &fakeArtifactStore{}, testObjects(t), nil, Options{DevHeaders: true}))
	t.Cleanup(server.Close)

	body := `{"format":"mp4","timelineKey":"tenant-1/project-1/narration/timeline/tl.json",
		"pagePngKeys":["tenant-1/project-1/render/page/p1.png"],
		"burnSubtitles":true,
		"animation":{"specs":[{"slideId":"slide-1","language":"zh-CN","sourceRevisionNo":2,"revision":5}]}}`
	resp := postBundleExport(t, server.URL, body, "anim-export-1")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	var snapshot app.ExportSnapshot
	if err := json.Unmarshal([]byte(jobs.inputSnapshot), &snapshot); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.Animation == nil || len(snapshot.Animation.Specs) != 1 {
		t.Fatalf("snapshot.animation = %+v", snapshot.Animation)
	}
	ref := snapshot.Animation.Specs[0]
	if ref.SlideID != "slide-1" || ref.Revision != 5 || ref.SourceRevisionNo != 2 || ref.Language != "zh-CN" {
		t.Fatalf("spec = %+v", ref)
	}
	// 顺带守护本轮补齐的入口等价性：REST 入口此前丢掉 burnSubtitles，
	// 同一种 mp4 从两个入口导出会得到不同的字幕行为。
	if !snapshot.BurnSubtitles {
		t.Fatal("burnSubtitles 未落进快照（REST 与 Connect 入口语义必须一致）")
	}
	if !strings.Contains(jobs.inputSnapshot, `"animation"`) {
		t.Fatalf("snapshot 字节里没有 animation: %s", jobs.inputSnapshot)
	}
}

// TestBundleExportStaticSnapshotHasNoAnimationBytes：不传动画时快照字节必须与动画字段
// 引入之前一致。这是 2.1 那条回归用例在入口侧的对应断言——入口多做一次归一是
// 最容易让"关掉动画"和"从来没这个功能"算出两个键的地方。
func TestBundleExportStaticSnapshotHasNoAnimationBytes(t *testing.T) {
	jobs := &jobCreatorStub{}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, jobs, &fakeArtifactStore{}, testObjects(t), nil, Options{DevHeaders: true}))
	t.Cleanup(server.Close)

	body := `{"format":"mp4","timelineKey":"tenant-1/project-1/narration/timeline/tl.json",
		"pagePngKeys":["tenant-1/project-1/render/page/p1.png"],"animation":{"specs":[]}}`
	resp := postBundleExport(t, server.URL, body, "anim-export-empty")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	if strings.Contains(jobs.inputSnapshot, "animation") {
		t.Fatalf("空 specs 不应进快照: %s", jobs.inputSnapshot)
	}
}

func TestBundleExportRejectsAnimationOnHTMLBundle(t *testing.T) {
	jobs := &jobCreatorStub{}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, jobs, &fakeArtifactStore{}, testObjects(t), nil, Options{DevHeaders: true}))
	t.Cleanup(server.Close)

	body := `{"format":"html_bundle","timelineKey":"tenant-1/project-1/narration/timeline/tl.json",
		"animation":{"specs":[{"slideId":"slide-1","revision":1}]}}`
	resp := postBundleExport(t, server.URL, body, "anim-export-html")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400（html_bundle 不产视频，接受动画等于承诺一个做不到的效果）", resp.StatusCode)
	}
	if jobs.inputSnapshot != "" {
		t.Fatal("被拒绝的请求不应创建任务")
	}
}

func TestCachedProbeHonorsTTL(t *testing.T) {
	probe := &stubProbe{}
	now := time.Unix(1000, 0)
	cp := &cachedProbe{client: probe, ttl: 10 * time.Second, now: func() time.Time { return now }}

	for i := 0; i < 3; i++ {
		_ = cp.Check(context.Background())
	}
	if probe.calls != 1 {
		t.Fatalf("probe calls = %d, want 1 within TTL", probe.calls)
	}
	now = now.Add(11 * time.Second)
	_ = cp.Check(context.Background())
	if probe.calls != 2 {
		t.Fatalf("probe calls = %d, want 2 after TTL（服务修好后不重启也要能变绿）", probe.calls)
	}
}

// ---- helpers ----

func getAnimationOptions(t *testing.T, baseURL, timelineKey string) exportAnimationOptions {
	t.Helper()
	endpoint := baseURL + "/api/projects/project-1/exports/animation-options"
	if timelineKey != "" {
		endpoint += "?timelineKey=" + url.QueryEscape(timelineKey)
	}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
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
		t.Fatalf("GET animation-options = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("content-type = %q（说明命中了 SPA 兜底而不是端点）", ct)
	}
	var out exportAnimationOptions
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func postBundleExport(t *testing.T, baseURL, body, key string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/projects/project-1/exports", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(tenantHeader, "tenant-1")
	req.Header.Set(userHeader, "user-1")
	req.Header.Set("Idempotency-Key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
