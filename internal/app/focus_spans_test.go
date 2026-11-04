package app

import (
	"context"
	"testing"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/render"
)

// emuPerPxAt1080 是 1920×1080 画布下的 EMU/px（6858000/1080 = 6350）。
// 测试里直接用整倍数，读数就是人手算得出来的那种，不必对着浮点结果反推。
const emuPerPxAt1080 = 6350

func emuToPx1080() float64 { return 1.0 / emuPerPxAt1080 }

func box(x, y, w, h int) render.EMUBox {
	return render.EMUBox{X: int64(x * emuPerPxAt1080), Y: int64(y * emuPerPxAt1080),
		Width: int64(w * emuPerPxAt1080), Height: int64(h * emuPerPxAt1080)}
}

// pageStartUS 让全局钟与页内钟刻意不相等：焦点算的是"相对本页起点"，不是绝对时刻。
const pageStartUS int64 = 1_000_000

// slideCue 造一页时间轴：段起点用毫秒给（相对本页），页起点固定为 1s。
func slideCue(segs ...media.SegmentCue) media.SlideCue {
	out := media.SlideCue{SlideID: "slide-1", StartUS: pageStartUS, EndUS: pageStartUS}
	for _, s := range segs {
		out.Segments = append(out.Segments, s)
		out.EndUS = pageStartUS + s.EndUS
	}
	return out
}

func cue(startMs, endMs int64, focus *media.SegmentFocus) media.SegmentCue {
	return media.SegmentCue{SegmentID: "seg", StartUS: pageStartUS + startMs*1000,
		EndUS: pageStartUS + endMs*1000, Focus: focus}
}

// TestFocusSpanPointsAtBar 是最小闭环：一段话指一根柱子，得到的矩形就是那根柱子的框。
func TestFocusSpanPointsAtBar(t *testing.T) {
	boxes := []FocusBox{
		{ShapeID: "shape-7", Box: box(100, 100, 800, 500)},
		{ShapeID: "shape-7", BarID: "b1", Box: box(200, 300, 60, 200)},
		{ShapeID: "shape-7", BarID: "b2", Box: box(300, 250, 60, 250)},
	}
	slide := slideCue(cue(0, 1000, &media.SegmentFocus{ShapeID: "shape-7", BarID: "b2"}))
	spans, diags := buildFocusSpans(focusCues(slide), boxes, nil, 2000, emuToPx1080())
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", diags)
	}
	if len(spans) != 1 {
		t.Fatalf("want 1 span, got %d", len(spans))
	}
	s := spans[0]
	if s.ElementID != "shape-7#b2" {
		t.Fatalf("elementId = %q, want shape-7#b2", s.ElementID)
	}
	if s.Rect == nil {
		t.Fatal("柱子没有图层，必须带矩形")
	}
	if *s.Rect != (render.Box{X: 300, Y: 250, W: 60, H: 250}) {
		t.Fatalf("rect = %+v, want 300,250 60x250", *s.Rect)
	}
	if s.StartMs != 0 {
		t.Fatalf("start = %d, want 0 (页起点)", s.StartMs)
	}
}

// TestFocusSpanShapeUsesLayerWhenAvailable：形状本来就是图层时不该改用矩形遮罩——
// 两种表达在画面上的观感不同（压暗其它层 vs 整页盖白留孔），能压暗就别盖白。
func TestFocusSpanShapeUsesLayerWhenAvailable(t *testing.T) {
	boxes := []FocusBox{{ShapeID: "shape-3", Box: box(100, 100, 400, 200)}}
	layers := map[string]struct{}{"shape-3": {}}
	slide := slideCue(cue(0, 500, &media.SegmentFocus{ShapeID: "shape-3"}))

	spans, diags := buildFocusSpans(focusCues(slide), boxes, layers, 2000, emuToPx1080())
	if len(diags) != 0 || len(spans) != 1 {
		t.Fatalf("spans=%+v diags=%+v", spans, diags)
	}
	if spans[0].Rect != nil {
		t.Fatalf("形状是图层时不该带矩形，got %+v", *spans[0].Rect)
	}
	if spans[0].ElementID != "shape-3" {
		t.Fatalf("elementId = %q", spans[0].ElementID)
	}

	// 没有图层（例如图表整块）→ 退到矩形。
	spans, diags = buildFocusSpans(focusCues(slide), boxes, nil, 2000, emuToPx1080())
	if len(diags) != 0 || len(spans) != 1 || spans[0].Rect == nil {
		t.Fatalf("非图层形状必须带矩形: spans=%+v diags=%+v", spans, diags)
	}
}

// TestFocusEndHoldsWhenNextSegmentAlsoPoints 锁住"句间不闪"规则。
func TestFocusEndHoldsWhenNextSegmentAlsoPoints(t *testing.T) {
	boxes := []FocusBox{{ShapeID: "shape-1", BarID: "b1", Box: box(10, 10, 50, 50)},
		{ShapeID: "shape-1", BarID: "b2", Box: box(70, 10, 50, 50)}}
	slide := slideCue(
		cue(0, 900, &media.SegmentFocus{ShapeID: "shape-1", BarID: "b1"}),
		cue(1200, 2000, &media.SegmentFocus{ShapeID: "shape-1", BarID: "b2"}),
	)
	spans, _ := buildFocusSpans(focusCues(slide), boxes, nil, 2500, emuToPx1080())
	if len(spans) != 2 {
		t.Fatalf("want 2 spans, got %+v", spans)
	}
	if spans[0].EndMs != 0 {
		t.Fatalf("下一段也指东西时应保持（EndMs=0），got %d", spans[0].EndMs)
	}
	// 最后一段：句末放手，页尾余量（2500-2000）整页全亮——转场前先放掉焦点，
	// 而不是让遮罩一直挂到切页。
	if spans[1].EndMs != 2000 {
		t.Fatalf("最后一段 EndMs = %d, want 2000", spans[1].EndMs)
	}
}

// TestFocusEndReleasesWhenTalkMovesOn：下一段不指任何东西时，在句末放手。
//
// 不放手的话画面会一直指着一个已经讲完的对象；而每句都放手会让整页每隔一句
// 闪一下全亮。这两条都不是"无所谓"的观感问题，所以各有一条用例。
func TestFocusEndReleasesWhenTalkMovesOn(t *testing.T) {
	boxes := []FocusBox{{ShapeID: "shape-1", BarID: "b1", Box: box(10, 10, 50, 50)}}
	slide := slideCue(
		cue(0, 900, &media.SegmentFocus{ShapeID: "shape-1", BarID: "b1"}),
		cue(1100, 1800, nil),
	)
	spans, _ := buildFocusSpans(focusCues(slide), boxes, nil, 2000, emuToPx1080())
	if len(spans) != 1 {
		t.Fatalf("want 1 span, got %+v", spans)
	}
	if spans[0].EndMs != 900 {
		t.Fatalf("EndMs = %d, want 900（句末放手）", spans[0].EndMs)
	}
}

// TestFocusTargetMissingIsDiagnosticNotFailure：目标写错不能让整页编译失败。
func TestFocusTargetMissingIsDiagnosticNotFailure(t *testing.T) {
	slide := slideCue(cue(0, 900, &media.SegmentFocus{ShapeID: "shape-404", BarID: "b9"}))
	spans, diags := buildFocusSpans(focusCues(slide), nil, nil, 2000, emuToPx1080())
	if len(spans) != 0 {
		t.Fatalf("不该产出焦点: %+v", spans)
	}
	if len(diags) != 1 || diags[0].Code != render.DiagFocusTargetMissing {
		t.Fatalf("want one focus_target_missing, got %+v", diags)
	}
}

// TestFocusDuplicateTargetKeepsFirst：渲染器要求焦点互斥（按 elementId 求压暗量），
// 同一页同一目标出现两次时只能留第一段，并且必须留痕。
func TestFocusDuplicateTargetKeepsFirst(t *testing.T) {
	boxes := []FocusBox{{ShapeID: "shape-1", BarID: "b1", Box: box(10, 10, 50, 50)}}
	f := &media.SegmentFocus{ShapeID: "shape-1", BarID: "b1"}
	slide := slideCue(cue(0, 500, f), cue(600, 900, nil), cue(1000, 1500, f))
	spans, diags := buildFocusSpans(focusCues(slide), boxes, nil, 2000, emuToPx1080())
	if len(spans) != 1 || spans[0].StartMs != 0 {
		t.Fatalf("应只保留第一段: %+v", spans)
	}
	if len(diags) != 1 || diags[0].Code != render.DiagFocusDuplicate {
		t.Fatalf("want focus_duplicate, got %+v", diags)
	}
}

// TestFocusRectScalesWithCanvas：焦点矩形按导出画布缩放，不是写死 1920。
//
// 这一条容易漏：矩形是在解析画布（1920×1080）里量出来的，而导出可能是 1280×720。
// 直接拿原值会让整块遮罩缩小到 2/3 的位置，看起来"框住了一片空地"。
func TestFocusRectScalesWithCanvas(t *testing.T) {
	boxes := []FocusBox{{ShapeID: "shape-1", BarID: "b1", Box: box(100, 100, 200, 100)}}
	slide := slideCue(cue(0, 500, &media.SegmentFocus{ShapeID: "shape-1", BarID: "b1"}))

	spans, _ := buildFocusSpans(focusCues(slide), boxes, nil, 2000, emuToPx1080())
	if *spans[0].Rect != (render.Box{X: 100, Y: 100, W: 200, H: 100}) {
		t.Fatalf("1080p: %+v", *spans[0].Rect)
	}
	// 720p 画布：EMU→px 系数按 1080/6858000 推出，同一块矩形应缩到 2/3。
	spans, _ = buildFocusSpans(focusCues(slide), boxes, nil, 2000, emuToPx1080()*2.0/3.0)
	got := *spans[0].Rect
	if got.X != 67 || got.W != 133 {
		t.Fatalf("720p: %+v，want x=67 w=133（四舍五入）", got)
	}
}

// TestFocusStartsOutsidePageIsSkipped：起点落在页外时跳过，而不是让它把整页校验拖失败。
func TestFocusStartsOutsidePageIsSkipped(t *testing.T) {
	boxes := []FocusBox{{ShapeID: "shape-1", BarID: "b1", Box: box(10, 10, 50, 50)}}
	slide := slideCue(cue(2500, 3000, &media.SegmentFocus{ShapeID: "shape-1", BarID: "b1"}))
	spans, diags := buildFocusSpans(focusCues(slide), boxes, nil, 2000, emuToPx1080())
	if len(spans) != 0 || len(diags) != 1 {
		t.Fatalf("spans=%+v diags=%+v", spans, diags)
	}
}

type fakeFocusSource struct {
	revs  []int
	boxes map[string][]FocusBox
}

func (f *fakeFocusSource) FocusBoxes(_ context.Context, scope PageGeometryScope) (map[string][]FocusBox, error) {
	f.revs = append(f.revs, scope.SourceRevisionNo)
	return f.boxes, nil
}

type nilSpecs struct{}

func (nilSpecs) Get(context.Context, animation.Key) (*animation.Revision, error) { return nil, nil }

// TestAssemblerFocusIgnoresPerPageSpec 锁住「焦点与这一页配没配动效无关」。
//
// 第 2 页没有动画规格，但讲稿写了"看这根柱子"——它仍然要指。实现上靠全片源版本号兜底：
// 若跟着本页规格走，第 2 页拿到的版本号是 0，读不到文档，焦点就静默消失。
func TestAssemblerFocusIgnoresPerPageSpec(t *testing.T) {
	src := &fakeFocusSource{boxes: map[string][]FocusBox{
		"slide-2": {{ShapeID: "shape-1", BarID: "b1", Box: box(10, 10, 50, 50)}},
	}}
	as := NewAnimationPageAssembler(nil, nilSpecs{}).WithFocus(src)
	png := []byte{0x89, 'P', 'N', 'G'}
	req, err := as.Build(context.Background(), AnimationPageInput{
		Snapshot: ExportSnapshot{
			Width: 1920, Height: 1080, FPS: 30,
			Animation: &AnimationOptions{Specs: []AnimationSpecRef{{SlideID: "slide-1", SourceRevisionNo: 7, Revision: 1}}},
		},
		Slides: []media.SlideCue{
			{SlideID: "slide-1", StartUS: 0, EndUS: 1_000_000,
				Segments: []media.SegmentCue{{SegmentID: "s1", StartUS: 0, EndUS: 1_000_000}}},
			{SlideID: "slide-2", StartUS: 1_000_000, EndUS: 3_000_000,
				Segments: []media.SegmentCue{{SegmentID: "s2", StartUS: 1_000_000, EndUS: 2_000_000,
					Focus: &media.SegmentFocus{ShapeID: "shape-1", BarID: "b1"}}}},
		},
		PagePNGs:       [][]byte{png, png},
		BaseImagePaths: []string{"slides/001.png", "slides/002.png"},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(src.revs) != 1 || src.revs[0] != 7 {
		t.Fatalf("焦点应按全片源版本 7 读文档，实际 %v", src.revs)
	}
	if len(req.Pages[1].Focus) != 1 {
		t.Fatalf("没有动效规格的页也要有焦点，got %+v", req.Pages[1].Focus)
	}
}
