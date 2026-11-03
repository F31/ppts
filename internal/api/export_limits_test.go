package api

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/tenant"
)

// 免费层（ADR-029 ②）的三条判据。分别对应三种不同的正确行为：
//
//  1. 分辨率**夹紧**（用户没选过它，拒绝等于让免费用户完全无法导出）；
//  2. 时长**拒绝**（压短就是改内容，且必须在建任务之前拒绝）；
//  3. 水印**写进快照**（它改变产物，必须改变产物身份，否则去重会串）。

func TestClampShortSide(t *testing.T) {
	cases := []struct {
		name           string
		w, h, maxShort int
		wantW, wantH   int
	}{
		{name: "720p 横屏", w: 1920, h: 1080, maxShort: 720, wantW: 1280, wantH: 720},
		{name: "720p 竖屏", w: 1080, h: 1920, maxShort: 720, wantW: 720, wantH: 1280},
		// 短边口径的关键用例：4:3 的 1440×1080 也该变成 960×720，
		// 而不是"按高度算"得到的 1280×720（后者已经被短边 720 限制住了，但比例会变）。
		{name: "720p 四比三", w: 1440, h: 1080, maxShort: 720, wantW: 960, wantH: 720},
		{name: "已经更小则不动", w: 854, h: 480, maxShort: 720, wantW: 854, wantH: 480},
		{name: "不限", w: 1920, h: 1080, maxShort: 0, wantW: 1920, wantH: 1080},
		{name: "正方形", w: 1080, h: 1080, maxShort: 720, wantW: 720, wantH: 720},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, h := clampShortSide(tc.w, tc.h, tc.maxShort)
			if w != tc.wantW || h != tc.wantH {
				t.Fatalf("clampShortSide(%d,%d,%d) = %dx%d, want %dx%d",
					tc.w, tc.h, tc.maxShort, w, h, tc.wantW, tc.wantH)
			}
			if w%2 != 0 || h%2 != 0 {
				t.Fatalf("夹紧后出现奇数尺寸 %dx%d（H.264 会直接拒绝）", w, h)
			}
		})
	}
}

// TestEnforceExportLimitsClampsAndWatermarks 守"夹紧 + 水印"两件事同时生效。
func TestEnforceExportLimitsClampsAndWatermarks(t *testing.T) {
	caps := animation.Capability{Enabled: true, MaxShortSide: 720, Watermark: "由智讲PPT生成"}

	t.Run("横屏夹到 720p 并带水印", func(t *testing.T) {
		snapshot := &app.ExportSnapshot{Format: artifactMP4, Width: 1920, Height: 1080}
		if err := enforceExportLimits(context.Background(), testObjects(t), "tenant-1", "k", caps, snapshot); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if snapshot.Width != 1280 || snapshot.Height != 720 {
			t.Fatalf("尺寸 = %dx%d, want 1280x720", snapshot.Width, snapshot.Height)
		}
		if snapshot.Watermark != "由智讲PPT生成" {
			t.Fatalf("水印 = %q, want 文案", snapshot.Watermark)
		}
	})

	// 字幕导出是纯文本，没有像素可打：不能因为套餐带水印就给它写一个水印字段，
	// 否则同一份字幕在"免费/付费"两种策略下会算出两个快照键，去重表里凭空多一份。
	t.Run("纯文本格式不受影响", func(t *testing.T) {
		snapshot := &app.ExportSnapshot{Format: "srt", Width: 1920, Height: 1080}
		if err := enforceExportLimits(context.Background(), testObjects(t), "tenant-1", "k", caps, snapshot); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if snapshot.Watermark != "" || snapshot.Width != 1920 {
			t.Fatalf("srt 不应被改：watermark=%q size=%dx%d", snapshot.Watermark, snapshot.Width, snapshot.Height)
		}
	})

	// 未配置套餐时不夹也不加：存量租户的导出必须逐字节不变。
	t.Run("未配置限制时原样", func(t *testing.T) {
		snapshot := &app.ExportSnapshot{Format: artifactMP4, Width: 1920, Height: 1080}
		if err := enforceExportLimits(context.Background(), testObjects(t), "tenant-1", "k", animation.Capability{Enabled: true}, snapshot); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if snapshot.Width != 1920 || snapshot.Height != 1080 || snapshot.Watermark != "" {
			t.Fatalf("不该改：%dx%d watermark=%q", snapshot.Width, snapshot.Height, snapshot.Watermark)
		}
	})
}

// artifactMP4 避免测试里为了一个字面量引入 artifact 包的全部符号。
const artifactMP4 = "mp4"

// TestEnforceExportLimitsRejectsLongTimeline 守"超时长必须拒绝，且带数字"。
func TestEnforceExportLimitsRejectsLongTimeline(t *testing.T) {
	objects := testObjects(t)
	// 4 分钟的时间轴，套餐限 3 分钟。
	putTimeline(t, objects, "tenant-1/project-1/narration/timeline/tl.json", 240_000_000)

	snapshot := &app.ExportSnapshot{Format: artifactMP4, Width: 1920, Height: 1080}
	err := enforceExportLimits(context.Background(), objects, "tenant-1",
		"tenant-1/project-1/narration/timeline/tl.json",
		animation.Capability{Enabled: true, MaxDurationSec: 180}, snapshot)
	if err == nil {
		t.Fatal("4 分钟的片子应当被拒绝")
	}
	var overdur *exportDurationExceeded
	if !asDurationExceeded(err, &overdur) {
		t.Fatalf("错误类型 = %T, want *exportDurationExceeded", err)
	}
	if overdur.LimitSec != 180 {
		t.Fatalf("上限 = %d, want 180", overdur.LimitSec)
	}
	if !strings.Contains(overdur.Error(), "4 分 00 秒") || !strings.Contains(overdur.Error(), "3 分 00 秒") {
		// 文案里必须有数字：只说"超过上限"，用户与支持都不知道差多少。
		t.Fatalf("错误文案缺少数字：%s", overdur.Error())
	}

	// 刚好卡在上限的片子要放行（含 1 秒舍入余量）。
	putTimeline(t, objects, "tenant-1/project-1/narration/timeline/ok.json", 180_000_000)
	snapshot = &app.ExportSnapshot{Format: artifactMP4, Width: 1920, Height: 1080}
	if err := enforceExportLimits(context.Background(), objects, "tenant-1",
		"tenant-1/project-1/narration/timeline/ok.json",
		animation.Capability{Enabled: true, MaxDurationSec: 180}, snapshot); err != nil {
		t.Fatalf("卡在上限的片子不该被拒：%v", err)
	}

	// 时间轴读不出来时放行：导出本身会在 worker 里失败，
	// 在这里因为"读不到"拒绝，会把一次存储抖动说成"你的片子太长"。
	snapshot = &app.ExportSnapshot{Format: artifactMP4, Width: 1920, Height: 1080}
	if err := enforceExportLimits(context.Background(), objects, "tenant-1",
		"tenant-1/project-1/narration/timeline/missing.json",
		animation.Capability{Enabled: true, MaxDurationSec: 180}, snapshot); err != nil {
		t.Fatalf("读不到时间轴时应放行：%v", err)
	}
}

// TestBundleExportFreeTierClampsAndWatermarks 走完整 REST 入口。
// 这条守的是"限制真的发生在受理时、且写进了快照"——写在 worker 里就等于
// 用户排了队才被告知，而写晚一步就进不了 snapshotHash。
func TestBundleExportFreeTierClampsAndWatermarks(t *testing.T) {
	objects := testObjects(t)
	putPage(t, objects, "tenant-1/project-1/render/page/p1.png", 16, 9)
	putTimeline(t, objects, "tenant-1/project-1/narration/timeline/tl.json", 60_000_000)

	yes := true
	policy := &tenant.Policy{Animation: &animation.AnimationPolicy{
		Enabled: &yes, MaxHeight: intPtr(720), Watermark: strPtr("由智讲PPT生成"),
	}}
	jobs := &jobCreatorStub{}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, jobs,
		&fakeArtifactStore{}, objects, nil, Options{
			DevHeaders: true, Policy: fakePolicyReader{policy: policy}, AnimationEnabled: true,
		}))
	t.Cleanup(server.Close)

	body := `{"format":"html_bundle","timelineKey":"tenant-1/project-1/narration/timeline/tl.json","pagePngKeys":["tenant-1/project-1/render/page/p1.png"]}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/projects/project-1/exports", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(tenantHeader, "tenant-1")
	req.Header.Set(userHeader, "user-1")
	req.Header.Set("Idempotency-Key", "free-tier-1")
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
	if snapshot.Width != 1280 || snapshot.Height != 720 {
		t.Fatalf("免费层应夹到 1280x720，得到 %dx%d", snapshot.Width, snapshot.Height)
	}
	if snapshot.Watermark != "由智讲PPT生成" {
		t.Fatalf("快照没带水印：%q", snapshot.Watermark)
	}
}

// TestBundleExportRejectsOverlongTimeline 守"超时长在受理时拒绝，不建任务"。
// 断言 jobs 未被调用：这是"静默排队"与"明确拒绝"的唯一可判别差异。
func TestBundleExportRejectsOverlongTimeline(t *testing.T) {
	objects := testObjects(t)
	putPage(t, objects, "tenant-1/project-1/render/page/p1.png", 16, 9)
	putTimeline(t, objects, "tenant-1/project-1/narration/timeline/tl.json", 360_000_000) // 6 分钟

	yes := true
	policy := &tenant.Policy{Animation: &animation.AnimationPolicy{
		Enabled: &yes, MaxDurationSec: intPtr(180), Watermark: strPtr("由智讲PPT生成"),
	}}
	jobs := &jobCreatorStub{}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, jobs,
		&fakeArtifactStore{}, objects, nil, Options{
			DevHeaders: true, Policy: fakePolicyReader{policy: policy}, AnimationEnabled: true,
		}))
	t.Cleanup(server.Close)

	body := `{"format":"mp4","timelineKey":"tenant-1/project-1/narration/timeline/tl.json","pagePngKeys":["tenant-1/project-1/render/page/p1.png"]}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/projects/project-1/exports", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(tenantHeader, "tenant-1")
	req.Header.Set(userHeader, "user-1")
	req.Header.Set("Idempotency-Key", "overlong-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	var payload struct {
		Code          string  `json:"code"`
		LimitSeconds  int     `json:"limitSeconds"`
		ActualSeconds float64 `json:"actualSeconds"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Code != "export_duration_exceeded" || payload.LimitSeconds != 180 || payload.ActualSeconds != 360 {
		t.Fatalf("响应体 = %+v，需要带 code/上限/实际值", payload)
	}
	if jobs.inputSnapshot != "" {
		t.Fatal("超时长的请求不应建任务（设计 §7.5 禁止静默排队）")
	}
}

func strPtr(v string) *string { return &v }

// asDurationExceeded 避免在测试里直接依赖 errors.As 的泛型感（与生产代码同一判据）。
func asDurationExceeded(err error, target **exportDurationExceeded) bool {
	if e, ok := err.(*exportDurationExceeded); ok {
		*target = e
		return true
	}
	return false
}

func putTimeline(t *testing.T, objects objectstore.ObjectStore, rawKey string, durationUS int64) {
	t.Helper()
	payload, err := json.Marshal(app.TimelineAsset{Timeline: &media.Timeline{DurationUS: durationUS}})
	if err != nil {
		t.Fatalf("marshal timeline: %v", err)
	}
	key, err := objectstore.Parse(rawKey)
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}
	if err := objects.Put(context.Background(), key, bytes.NewReader(payload),
		objectstore.ObjectMeta{ContentType: "application/json", Size: int64(len(payload))}); err != nil {
		t.Fatalf("put timeline: %v", err)
	}
}

func putPage(t *testing.T, objects objectstore.ObjectStore, rawKey string, w, h int) {
	t.Helper()
	var buf bytes.Buffer
	// 只需要图头里的两个整数：像素内容是什么无关紧要。
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	key, err := objectstore.Parse(rawKey)
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}
	if err := objects.Put(context.Background(), key, bytes.NewReader(buf.Bytes()),
		objectstore.ObjectMeta{ContentType: "image/png", Size: int64(buf.Len())}); err != nil {
		t.Fatalf("put page: %v", err)
	}
}

// TestConnectExportDurationExceededKeepsNumbers 守 Connect 入口的同一判定：
// 它不能让时长超限退化成"服务内部错误"——那样前端只会显示一句无信息量的失败，
// 而真正需要告诉用户的是"上限 3 分钟，你这条 6 分钟"。
func TestConnectExportDurationExceededKeepsNumbers(t *testing.T) {
	err := connectExportDurationExceeded(&exportDurationExceeded{LimitSec: 180, ActualSec: 360})
	if err == nil {
		t.Fatal("应返回错误")
	}
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Fatalf("code = %v, want %v", got, connect.CodeFailedPrecondition)
	}
	if !strings.Contains(err.Message(), "3 分 00 秒") || !strings.Contains(err.Message(), "6 分 00 秒") {
		t.Fatalf("消息应带上限与实际值：%q", err.Message())
	}
}
