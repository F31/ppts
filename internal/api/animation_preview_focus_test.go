package api

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"testing"

	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/internal/design"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
)

// chartPageForAPI 与 animationTestPage 同一页，但多一张**切好柱子**的图表。
// 柱子框是解析画布像素（1920×1080 基准），预览必须按自己的 960 画布重新折算。
func chartPageForAPI() *project.Page {
	return &project.Page{
		Index:   2,
		SlideID: anTestSlide,
		Shapes: []*project.Shape{
			{ID: "shape-1", Kind: "text", Text: "第三季度回顾", Bounds: &project.Box{X: 800000, Y: 600000, Width: 8000000, Height: 900000}},
			{ID: "shape-7", Kind: "chart", Bounds: &project.Box{X: 800000, Y: 2000000, Width: 8000000, Height: 4000000},
				ChartBars: []project.ChartBar{
					{ID: "b1", Category: "华东", Series: "耗时", Value: 174, Box: [4]int{300, 250, 60, 250}},
				}},
		},
	}
}

// rewriteTimelineWithFocus 覆盖写一份时间轴：第二页的第二段指 shape-7 的 b1。
//
// 不动 previewTimeline：它有六处调用，而「这一页有没有焦点」只有本文件的用例关心。
func rewriteTimelineWithFocus(t *testing.T, objects objectstore.ObjectStore, timelineKey string) {
	t.Helper()
	key, err := objectstore.Parse(timelineKey)
	if err != nil {
		t.Fatalf("parse timeline key: %v", err)
	}
	timeline, err := media.BuildTimeline([]media.SlideInput{
		{SlideID: "slide-1", Segments: []media.SegmentInput{
			{SegmentID: "seg-a", DisplayText: "第一页", AudioKey: "tenant-1/project-1/cache/audio/a.wav", DurationMS: 1000}}},
		{SlideID: anTestSlide, Segments: []media.SegmentInput{
			{SegmentID: "seg-1", DisplayText: "第二页前半", AudioKey: "tenant-1/project-1/cache/audio/b.wav", DurationMS: 1000},
			{SegmentID: "seg-2", DisplayText: "第二页后半", AudioKey: "tenant-1/project-1/cache/audio/c.wav", DurationMS: 1000,
				Focus: &media.SegmentFocus{ShapeID: "shape-7", BarID: "b1"}},
		}},
	}, media.Timing{})
	if err != nil {
		t.Fatalf("build timeline: %v", err)
	}
	bundle, err := json.Marshal(app.TimelineAsset{Timeline: timeline, SRTKey: "t/s.srt", VTTKey: "t/v.vtt"})
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}
	putAPIObject(t, objects, key, bundle, "application/json")
}

// rewritePageImage 把这一页的渲染图换成指定尺寸的纯色 PNG。
func rewritePageImage(t *testing.T, objects objectstore.ObjectStore, w, h int) {
	t.Helper()
	key := objectstore.ObjectKey{TenantID: db.LocalTenantID, ProjectID: anTestProject,
		Revision: "render", AssetType: "render", AssetID: anTestSlide, Ext: "png"}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(1, 1, color.RGBA{G: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	putAPIObject(t, objects, key, buf.Bytes(), "image/png")
}

// runPreviewFor 发一次预览请求并返回响应与提交给渲染服务的计划。
//
// after 在时间轴与页图都落盘之后调用：previewTimeline 会把页图写回默认的 96×72，
// 想换一张图必须在它之后覆盖——否则测的其实是默认图，而用例"通过"。
func runPreviewFor(t *testing.T, objects objectstore.ObjectStore, tweak func(*AnimationPreviewer),
	after func(*testing.T, objectstore.ObjectStore)) (*http.Response, []byte, []byte, *fakeRenderService) {
	t.Helper()
	timelineKey, manifestKey := previewTimeline(t, objects)
	rewriteTimelineWithFocus(t, objects, timelineKey)
	if after != nil {
		after(t, objects)
	}
	jobs := &narrationJobStub{job: &pipeline.Job{ID: "job-narr"}, stepRef: timelineKey,
		parseJob: &pipeline.Job{ID: "job-parse"}, pageRef: manifestKey}
	fake := newFakeRenderService(t)
	ts := newPreviewTestServer(t, objects, jobs, fake, tweak)

	body, _ := json.Marshal(map[string]any{"timelineKey": timelineKey})
	resp, err := http.Post(ts.URL+animationURL()+"/preview", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post preview: %v", err)
	}
	defer resp.Body.Close()
	raw := new(bytes.Buffer)
	_, _ = raw.ReadFrom(resp.Body)
	if fake.plans == nil {
		t.Fatalf("渲染服务没收到计划，status=%d body=%s", resp.StatusCode, raw.String())
	}
	return resp, raw.Bytes(), body, fake
}

// TestAnimationPreviewCarriesFocus 是这一段的闭环：预览计划里要真有焦点。
//
// 断言的是**换算后的像素**而不是"非空"：预览画布 960×720（页图 4:3），
// 柱子框以 1920×1080 为基准给，正确结果是坐标乘 2/3。沿用导出系数（1080）会得到
// 300/250，画面上遮罩偏到柱子右下角——而接口 200、日志干净，只能靠数值得证。
func TestAnimationPreviewCarriesFocus(t *testing.T) {
	objects := testObjects(t)
	resp, raw, _, fake := runPreviewFor(t, objects, func(p *AnimationPreviewer) {
		if r, ok := p.pages.(*fakeAnimationResolver); ok {
			r.page = chartPageForAPI()
		}
	}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("preview status = %d, body = %s", resp.StatusCode, raw)
	}

	plan := fake.plans[0]
	if len(plan.Pages) != 1 {
		t.Fatalf("want 1 page, got %d", len(plan.Pages))
	}
	focus := plan.Pages[0].Focus
	if len(focus) != 1 {
		t.Fatalf("预览计划里应有 1 条焦点，got %+v", focus)
	}
	if focus[0].ElementID != "shape-7#b1" {
		t.Fatalf("elementId = %q, want shape-7#b1", focus[0].ElementID)
	}
	if focus[0].Rect == nil {
		t.Fatal("柱子没有图层，焦点必须带矩形")
	}
	want := [4]int{200, 167, 40, 167}
	got := *focus[0].Rect
	if got.X != want[0] || got.Y != want[1] || got.W != want[2] || got.H != want[3] {
		t.Fatalf("rect = %+v，want %v（960×720 画布：解析像素 ×2/3）", got, want)
	}
	// 第二段开口于 1000ms，最后一段指到页末（不给终点）。
	if focus[0].StartMs != 1000 || focus[0].EndMs != 0 {
		t.Fatalf("start/end = %d/%d，want 1000/0", focus[0].StartMs, focus[0].EndMs)
	}

	if got := resp.Header.Get("X-Preview-Focus"); got != "1" {
		t.Fatalf("X-Preview-Focus = %q, want 1", got)
	}
	if got := resp.Header.Get("X-Preview-Focus-Skipped"); got != "" {
		t.Fatalf("没有跳过时不该带 X-Preview-Focus-Skipped，got %q", got)
	}
}

// TestAnimationPreviewDropsFocusWhenCanvasAspectBreaks 守「宁可不错位」。
//
// previewCanvas 会把高度夹到 [240,1920]。一张 4000×200 的页图算出 48px 高，被抬到 240，
// 画布比例从此与页图无关——此时单一 EMU→像素系数会把框放歪。正确做法是这一页不指，
// 并如实说明原因；错误做法是照旧画一个位置错误的孔（画面上看起来"有框"，实际指错）。
func TestAnimationPreviewDropsFocusWhenCanvasAspectBreaks(t *testing.T) {
	objects := testObjects(t)
	resp, raw, _, fake := runPreviewFor(t, objects, func(p *AnimationPreviewer) {
		if r, ok := p.pages.(*fakeAnimationResolver); ok {
			r.page = chartPageForAPI()
		}
	}, func(t *testing.T, objects objectstore.ObjectStore) {
		// 4000×200：960 宽的画布算出 48px 高，被抬到下限 240，比例从此与页图无关。
		rewritePageImage(t, objects, 4000, 200)
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("preview status = %d, body = %s", resp.StatusCode, raw)
	}
	if n := len(fake.plans[0].Pages[0].Focus); n != 0 {
		t.Fatalf("比例被破坏时不该带焦点，got %d 条%+v", n, fake.plans[0].Height)
	}
	if got := resp.Header.Get("X-Preview-Focus"); got != "0" {
		t.Fatalf("X-Preview-Focus = %q, want 0", got)
	}
	if got := resp.Header.Get("X-Preview-Focus-Skip-Reason"); got != "canvas_aspect" {
		t.Fatalf("X-Preview-Focus-Skip-Reason = %q, want canvas_aspect", got)
	}
	if got := resp.Header.Get("X-Preview-Focus-Skipped"); got != "1" {
		t.Fatalf("X-Preview-Focus-Skipped = %q, want 1", got)
	}
}

// TestCanvasKeepsAspect 锁住判据本身：常见的两种页面比例都要通过，
// 只有真的被夹过才拒绝——否则正常 4:3 的 deck 会莫名其妙没有焦点。
func TestCanvasKeepsAspect(t *testing.T) {
	pngOf := func(w, h int) []byte {
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			t.Fatalf("encode: %v", err)
		}
		return buf.Bytes()
	}
	cases := []struct {
		name   string
		canvas design.Canvas
		png    []byte
		want   bool
	}{
		{"16:9 正好", design.Canvas{Width: 960, Height: 540}, pngOf(1920, 1080), true},
		{"4:3 正好", design.Canvas{Width: 960, Height: 720}, pngOf(96, 72), true},
		{"奇数高度被 +1", design.Canvas{Width: 960, Height: 481}, pngOf(2000, 1001), true},
		{"高度被抬到 240", design.Canvas{Width: 960, Height: 240}, pngOf(4000, 200), false},
		{"解不出页图", design.Canvas{Width: 960, Height: 540}, []byte("not a png"), false},
	}
	for _, tc := range cases {
		if got := canvasKeepsAspect(tc.canvas, tc.png); got != tc.want {
			t.Errorf("%s: canvasKeepsAspect = %v, want %v", tc.name, got, tc.want)
		}
	}
}
