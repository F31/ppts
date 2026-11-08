package app

import (
	"context"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/design"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/render"
)

// emuPerPxAt1080 是 1920×1080 画布下的 EMU/px（6858000/1080 = 6350）。
// 测试里直接用整倍数，读数就是人手算得出来的那种，不必对着浮点结果反推。
const emuPerPxAt1080 = 6350

// canvasAt 按页高给一个 16:9 画布。焦点换算的唯一自变量是画布高（EMU→px），
// 而「框在不在画布内」还要宽——所以测试传的是画布，不是一个系数。
func canvasAt(height int) design.Canvas {
	return design.Canvas{Width: height * 16 / 9, Height: height}
}

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
	spans, diags := buildFocusSpans(focusCues(slide), boxes, nil, 2000, canvasAt(1080))
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

	spans, diags := buildFocusSpans(focusCues(slide), boxes, layers, 2000, canvasAt(1080))
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
	spans, diags = buildFocusSpans(focusCues(slide), boxes, nil, 2000, canvasAt(1080))
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
	spans, _ := buildFocusSpans(focusCues(slide), boxes, nil, 2500, canvasAt(1080))
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
	spans, _ := buildFocusSpans(focusCues(slide), boxes, nil, 2000, canvasAt(1080))
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
	spans, diags := buildFocusSpans(focusCues(slide), nil, nil, 2000, canvasAt(1080))
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
	spans, diags := buildFocusSpans(focusCues(slide), boxes, nil, 2000, canvasAt(1080))
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

	spans, _ := buildFocusSpans(focusCues(slide), boxes, nil, 2000, canvasAt(1080))
	if *spans[0].Rect != (render.Box{X: 100, Y: 100, W: 200, H: 100}) {
		t.Fatalf("1080p: %+v", *spans[0].Rect)
	}
	// 720p 画布：EMU→px 系数按 1080/6858000 推出，同一块矩形应缩到 2/3。
	spans, _ = buildFocusSpans(focusCues(slide), boxes, nil, 2000, canvasAt(720))
	got := *spans[0].Rect
	if got.X != 67 || got.W != 133 {
		t.Fatalf("720p: %+v，want x=67 w=133（四舍五入）", got)
	}
}

// TestFocusStartsOutsidePageIsSkipped：起点落在页外时跳过，而不是让它把整页校验拖失败。
func TestFocusStartsOutsidePageIsSkipped(t *testing.T) {
	boxes := []FocusBox{{ShapeID: "shape-1", BarID: "b1", Box: box(10, 10, 50, 50)}}
	slide := slideCue(cue(2500, 3000, &media.SegmentFocus{ShapeID: "shape-1", BarID: "b1"}))
	spans, diags := buildFocusSpans(focusCues(slide), boxes, nil, 2000, canvasAt(1080))
	if len(spans) != 0 || len(diags) != 1 {
		t.Fatalf("spans=%+v diags=%+v", spans, diags)
	}
}

// TestFocusRectOutsideCanvasIsSkipped：整块落在画布外的目标不指，并留一条诊断。
//
// 遮罩的孔若开到画面外，合成器会把它钳在边缘——画面上是一块盖住左上角的白纱，
// 比「不指」更糟，而所有报告都是绿的。渲染器对同一个框的做法是「钳制后无面积就不
// 成层」（见 render 的 boxOf），焦点侧跟着它判。
func TestFocusRectOutsideCanvasIsSkipped(t *testing.T) {
	boxes := []FocusBox{{ShapeID: "shape-out", Box: box(2000, 200, 300, 100)}}
	slide := slideCue(cue(0, 1000, &media.SegmentFocus{ShapeID: "shape-out"}))
	spans, diags := buildFocusSpans(focusCues(slide), boxes, nil, 2000, canvasAt(1080))
	if len(spans) != 0 {
		t.Fatalf("目标整块在画布外时不该产出遮罩，got %+v", spans)
	}
	if len(diags) != 1 || diags[0].Code != render.DiagFocusTargetMissing {
		t.Fatalf("want 一条 %s，got %+v", render.DiagFocusTargetMissing, diags)
	}
	if !strings.Contains(diags[0].Message, "画布外") {
		t.Fatalf("诊断要说清是「画布外」，而不是让人以为框算坏了：%s", diags[0].Message)
	}

	// 部分越界仍然可用：这里不裁宽（渲染器会把层裁到画布内），而遮罩超出画面的那一截
	// 本来就被屏幕裁掉了，两种做法在成片里逐像素一致。
	boxes = []FocusBox{{ShapeID: "shape-edge", Box: box(1900, 200, 300, 100)}}
	slide = slideCue(cue(0, 1000, &media.SegmentFocus{ShapeID: "shape-edge"}))
	spans, diags = buildFocusSpans(focusCues(slide), boxes, nil, 2000, canvasAt(1080))
	if len(spans) != 1 || len(diags) != 0 {
		t.Fatalf("部分越界不该被丢掉：spans=%+v diags=%+v", spans, diags)
	}
	if got := *spans[0].Rect; got.X != 1900 || got.W != 300 {
		t.Fatalf("部分越界的矩形 = %+v，want x=1900 w=300", got)
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

// chartPage 造一页带一根切好的柱子的文档。柱子框的单位是**解析画布像素**
// （1920×1080 基准），与 document.json 里 chartBars[].box 一致。
func chartPage() *project.Page {
	return &project.Page{
		SlideID: "slide-1",
		Shapes: []*project.Shape{{
			ID: "shape-7", Kind: "chart",
			Bounds: &project.Box{X: 800000, Y: 600000, Width: 8000000, Height: 4000000},
			ChartBars: []project.ChartBar{
				{ID: "b1", Category: "华东", Series: "耗时", Value: 174, Box: [4]int{300, 250, 60, 250}},
				{ID: "b2", Category: "华北", Series: "耗时", Value: 131, Box: [4]int{420, 300, 60, 200}},
			},
		}},
	}
}

// TestPageFocusSpansScalesToPreviewCanvas 守「预览画布 960 宽」这一档。
//
// 导出是 1920 宽，预览是 960。若预览沿用导出侧的系数（1080 像素对应标准页高），
// 遮罩会大整整一倍，画面上是「孔开在柱子外面」，而接口返回 200、日志干净。
// 这条用例只在 960 下才露馅——所以断言的是「540 画布下的坐标恰是 1080 的一半」。
func TestPageFocusSpansScalesToPreviewCanvas(t *testing.T) {
	slide := slideCue(cue(0, 1000, &media.SegmentFocus{ShapeID: "shape-7", BarID: "b1"}))

	exported, diags := PageFocusSpans(chartPage(), slide, nil, 2000, canvasAt(1080))
	if len(diags) != 0 || len(exported) != 1 {
		t.Fatalf("1080 画布：spans=%+v diags=%+v", exported, diags)
	}
	if got := *exported[0].Rect; got != (render.Box{X: 300, Y: 250, W: 60, H: 250}) {
		t.Fatalf("1080 画布 rect = %+v，want 与解析画布像素一致 (300,250 60x250)", got)
	}

	previewed, diags := PageFocusSpans(chartPage(), slide, nil, 2000, canvasAt(540))
	if len(diags) != 0 || len(previewed) != 1 {
		t.Fatalf("540 画布：spans=%+v diags=%+v", previewed, diags)
	}
	if got := *previewed[0].Rect; got != (render.Box{X: 150, Y: 125, W: 30, H: 125}) {
		t.Fatalf("540 画布 rect = %+v，want 恰好是 1080 的一半 (150,125 30x125)", got)
	}
}

// TestPageFocusSpansWithoutFocusSkipsWork 守「没写焦点就什么都不算」。
// 返回值必须是 nil 而不是空切片：调用方据此决定要不要往计划里塞 Focus 字段。
func TestPageFocusSpansWithoutFocusSkipsWork(t *testing.T) {
	slide := slideCue(cue(0, 1000, nil))
	spans, diags := PageFocusSpans(chartPage(), slide, nil, 2000, canvasAt(960))
	if spans != nil || diags != nil {
		t.Fatalf("没有焦点时应返回 nil/nil，got %+v / %+v", spans, diags)
	}
	// page 为 nil 也不能 panic：预览的 Resolve 返回 nil 页时这一页本就不该渲。
	if spans, diags := PageFocusSpans(nil, slide, nil, 2000, canvasAt(960)); spans != nil || diags != nil {
		t.Fatalf("nil page 应返回 nil/nil，got %+v / %+v", spans, diags)
	}
}

// TestPageFocusSpansReportsMissingTarget 守「目标不在这一页时留诊断，而不是凭空造框」。
func TestPageFocusSpansReportsMissingTarget(t *testing.T) {
	slide := slideCue(cue(0, 1000, &media.SegmentFocus{ShapeID: "shape-7", BarID: "b9"}))
	spans, diags := PageFocusSpans(chartPage(), slide, nil, 2000, canvasAt(540))
	if len(spans) != 0 {
		t.Fatalf("目标缺失时不该产出焦点，got %+v", spans)
	}
	if len(diags) != 1 || diags[0].Code != render.DiagFocusTargetMissing {
		t.Fatalf("want 一条 %s 诊断，got %+v", render.DiagFocusTargetMissing, diags)
	}
}

// TestAssemblerFocusOnStaticShapeFallsBackToRect 守「指着没有动效的形状也要看得见」。
//
// 底板已经把**有轨道**的元素从整页图里挖掉做成了图层，只有那些层能被单独压暗；
// 没有轨道的元素仍烘在底板上，压暗它对画面没有任何作用——可它一直在 layerIDs 里
// （那里装的是「文档里有几个元素」，不是「这一版会出几个图层」），于是焦点照样
// 记一条、诊断为零、画面上什么都不变。指一段标题、而这一页只给图表配了动效，
// 就是这种情形。
func TestAssemblerFocusOnStaticShapeFallsBackToRect(t *testing.T) {
	pub := &recordingPublisher{}
	key := animation.Key{TenantID: "t1", ProjectID: "p1", SlideID: "s1", Language: "zh-CN", SourceRevisionNo: 2}
	// 规格只给 card 配了动效：title 仍烘在底板上，指它只能靠矩形遮罩。
	specs := &fakeSpecSource{revs: map[animation.Key]*animation.Revision{
		key: {
			TenantID: "t1", ProjectID: "p1", SlideID: "s1", Language: "zh-CN",
			SourceRevisionNo: 2, Revision: 7, Intensity: 1,
			Tracks: []*animation.Track{{
				ElementID: "card", Kind: "bullet",
				Clips: []*animation.Clip{{ID: "c1", Type: "fadeIn", StartMs: 0, DurationMs: 300, Easing: "easeOut", Trigger: "onEnter"}},
			}},
		},
	}}
	geo := &fakeGeometry{docs: map[int]map[string][]render.Element{
		2: {"s1": {
			{ID: "title", Kind: "title", Bounds: &render.EMUBox{X: 100 * emuPerPxAt1080, Y: 100 * emuPerPxAt1080, Width: 400 * emuPerPxAt1080, Height: 100 * emuPerPxAt1080}},
			{ID: "card", Kind: "bullet", Bounds: &render.EMUBox{X: 100 * emuPerPxAt1080, Y: 300 * emuPerPxAt1080, Width: 400 * emuPerPxAt1080, Height: 200 * emuPerPxAt1080}},
		}},
	}}
	src := &fakeFocusSource{boxes: map[string][]FocusBox{
		"s1": {{ShapeID: "title", Box: box(100, 100, 400, 100)}},
	}}
	a := NewAnimationPageAssembler(pub, specs).WithGeometry(geo).WithFocus(src)

	in := assemblerInput()
	// 用 1920×1080 画布：EMU/px 正好是 6350，断言里的坐标就是人手算得出的整数。
	in.Snapshot.Width, in.Snapshot.Height = 1920, 1080
	in.Slides[0].Segments = []media.SegmentCue{{
		SegmentID: "s1:seg1", StartUS: 1_000_000, EndUS: 3_000_000,
		Focus: &media.SegmentFocus{ShapeID: "title"},
	}}
	in.Slides[0].EndUS = 4_500_000
	req, err := a.Build(context.Background(), in)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(req.Pages[0].Focus) != 1 {
		t.Fatalf("焦点没生成：%+v", req.Pages[0].Focus)
	}
	span := req.Pages[0].Focus[0]
	if span.ElementID != "title" {
		t.Fatalf("elementId = %q", span.ElementID)
	}
	if span.Rect == nil {
		t.Fatal("title 没有轨道、不会成为图层，必须退到矩形遮罩——否则这一段指了等于没指")
	}
	if *span.Rect != (render.Box{X: 100, Y: 100, W: 400, H: 100}) {
		t.Fatalf("rect = %+v，want 100,100 400x100", *span.Rect)
	}
}

// TestAssemblerFocusOnAnimatedShapeKeepsLayer 是上一条的反面：真有轨道就别盖白。
func TestAssemblerFocusOnAnimatedShapeKeepsLayer(t *testing.T) {
	pub := &recordingPublisher{}
	key := animation.Key{TenantID: "t1", ProjectID: "p1", SlideID: "s1", Language: "zh-CN", SourceRevisionNo: 2}
	specs := &fakeSpecSource{revs: map[animation.Key]*animation.Revision{
		key: {
			TenantID: "t1", ProjectID: "p1", SlideID: "s1", Language: "zh-CN",
			SourceRevisionNo: 2, Revision: 7, Intensity: 1,
			Tracks: []*animation.Track{{
				ElementID: "title", Kind: "title",
				Clips: []*animation.Clip{{ID: "c1", Type: "fadeIn", StartMs: 0, DurationMs: 300, Easing: "easeOut", Trigger: "onEnter"}},
			}},
		},
	}}
	geo := &fakeGeometry{docs: map[int]map[string][]render.Element{
		2: {"s1": {{ID: "title", Kind: "title", Bounds: &render.EMUBox{X: 100 * emuPerPxAt1080, Y: 100 * emuPerPxAt1080, Width: 400 * emuPerPxAt1080, Height: 100 * emuPerPxAt1080}}}},
	}}
	src := &fakeFocusSource{boxes: map[string][]FocusBox{
		"s1": {{ShapeID: "title", Box: box(100, 100, 400, 100)}},
	}}
	a := NewAnimationPageAssembler(pub, specs).WithGeometry(geo).WithFocus(src)

	in := assemblerInput()
	in.Slides[0].Segments = []media.SegmentCue{{
		SegmentID: "s1:seg1", StartUS: 1_000_000, EndUS: 3_000_000,
		Focus: &media.SegmentFocus{ShapeID: "title"},
	}}
	req, err := a.Build(context.Background(), in)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(req.Pages[0].Focus) != 1 {
		t.Fatalf("焦点没生成：%+v", req.Pages[0].Focus)
	}
	if req.Pages[0].Focus[0].Rect != nil {
		t.Fatalf("真有轨道的形状应走图层压暗，不该盖白：%+v", *req.Pages[0].Focus[0].Rect)
	}
}

// textPage 造一页两个文字形状的文档（标题 + 卡片），用于「形状焦点」的用例。
func textPage() *project.Page {
	return &project.Page{
		SlideID: "slide-1",
		Shapes: []*project.Shape{
			{ID: "title", Kind: "shape", Text: "季度回顾",
				Bounds: &project.Box{X: 100 * emuPerPxAt1080, Y: 100 * emuPerPxAt1080,
					Width: 400 * emuPerPxAt1080, Height: 100 * emuPerPxAt1080}},
			{ID: "card", Kind: "shape", Text: "三条结论",
				Bounds: &project.Box{X: 100 * emuPerPxAt1080, Y: 300 * emuPerPxAt1080,
					Width: 400 * emuPerPxAt1080, Height: 200 * emuPerPxAt1080}},
		},
	}
}

// 预览与导出必须对同一个目标给出同一种表达：预览在压暗、成片在盖白，是最难查的一种
// 分叉（两边都不报错，只是看到的东西不一样）。这条用例把「有没有轨道」的判据钉在
// PageFocusSpans 上，与组装器侧的两条用例成对。
func TestPageFocusSpansUsesSpecTracks(t *testing.T) {
	slide := slideCue(cue(0, 1000, &media.SegmentFocus{ShapeID: "title"}))
	onlyCard := &animation.Revision{Tracks: []*animation.Track{{ElementID: "card", Kind: "bullet"}}}
	onlyTitle := &animation.Revision{Tracks: []*animation.Track{{ElementID: "title", Kind: "title"}}}

	// title 没有轨道 → 不会成为图层 → 必须盖白留孔。
	spans, diags := PageFocusSpans(textPage(), slide, onlyCard, 2000, canvasAt(1080))
	if len(diags) != 0 || len(spans) != 1 {
		t.Fatalf("spans=%+v diags=%+v", spans, diags)
	}
	if spans[0].Rect == nil || *spans[0].Rect != (render.Box{X: 100, Y: 100, W: 400, H: 100}) {
		t.Fatalf("title 无轨道时该退到矩形 100,100 400x100，got %+v", spans[0].Rect)
	}

	// title 有轨道 → 它是图层，压暗其它层就够，不必整页盖白。
	spans, diags = PageFocusSpans(textPage(), slide, onlyTitle, 2000, canvasAt(1080))
	if len(diags) != 0 || len(spans) != 1 {
		t.Fatalf("spans=%+v diags=%+v", spans, diags)
	}
	if spans[0].Rect != nil {
		t.Fatalf("title 有轨道时不该盖白，got %+v", *spans[0].Rect)
	}
}

// layerElementIDs 的判据就是这一条：**有轨道且有几何**。少任一条都会让焦点去等一个
// 永远不存在的层——等不到不会报错，只是画面上什么都不变。
func TestLayerElementIDs(t *testing.T) {
	boxed := func(id string) render.Element {
		return render.Element{ID: id, Bounds: &render.EMUBox{Width: 100 * emuPerPxAt1080, Height: 100 * emuPerPxAt1080}}
	}
	elements := []render.Element{
		boxed("title"), boxed("card"),
		// 有 ID、有轨道，但解析不出框：编译器会报 anchor_missing，不成层。
		{ID: "nogeo"},
		// 有框但宽高为 0（零面积）：toBox 同样按 anchor_missing 拒掉。
		{ID: "flat", Bounds: &render.EMUBox{X: 100, Y: 100}},
		// 框整块落在画布右边：钳制后宽度变负，编译器也算它不成层。
		{ID: "offright", Bounds: &render.EMUBox{
			X: int64(2100 * emuPerPxAt1080), Y: int64(100 * emuPerPxAt1080),
			Width: int64(200 * emuPerPxAt1080), Height: int64(100 * emuPerPxAt1080),
		}},
	}
	cases := []struct {
		name string
		rev  *animation.Revision
		want []string
	}{
		{"没有规格就一个层都没有", nil, nil},
		{"整页轨道不算元素层", &animation.Revision{Tracks: []*animation.Track{{ElementID: "", Kind: "page"}}}, nil},
		{"有轨道有几何 → 是层", &animation.Revision{Tracks: []*animation.Track{{ElementID: "card"}}}, []string{"card"}},
		{"轨道指向文档里没有的元素 → 不是层", &animation.Revision{Tracks: []*animation.Track{{ElementID: "ghost"}}}, nil},
		{"有轨道有 ID 但没几何 → 不是层", &animation.Revision{Tracks: []*animation.Track{{ElementID: "nogeo"}}}, nil},
		{"有轨道但框是零面积 → 不是层", &animation.Revision{Tracks: []*animation.Track{{ElementID: "flat"}}}, nil},
		{"有轨道但框整块在画布外 → 不是层", &animation.Revision{Tracks: []*animation.Track{{ElementID: "offright"}}}, nil},
		{"多条轨道去重", &animation.Revision{Tracks: []*animation.Track{
			{ElementID: "card"}, {ElementID: "card"}, {ElementID: "title"},
		}}, []string{"card", "title"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := layerElementIDs(tc.rev, elements, canvasAt(1080))
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for _, id := range tc.want {
				if _, ok := got[id]; !ok {
					t.Fatalf("got %v, want 含 %q", got, id)
				}
			}
		})
	}
}

// TestLayerElementIDsMatchesCompiler 把「这一版到底会出几个图层」交给编译器来判。
//
// layerElementIDs 与 render 的成层规则是两份代码。焦点侧判成层，就等着靠压暗指它；
// 渲染侧若没出这一层，压暗的是一个不存在的东西——画面逐像素不变，而焦点记录、
// 清单、诊断全都正常（7053433 修的就是这一类）。上面那张手搭的表只能证明「判据按我
// 理解的样子写」，证明不了「和编译器一致」。
//
// 所以这里不重写判据，而是拿编译器当裁判：同一份输入交给 CompilePage，焦点侧认的
// 层集必须与 plan 里的 elementId 集逐项相同。哪天渲染侧多了成层的途径（G8 注入、
// 父层拆分……）而焦点侧没跟上，这条会先红。
func TestLayerElementIDsMatchesCompiler(t *testing.T) {
	page := func(px int) *render.EMUBox {
		return &render.EMUBox{
			X: int64(96 * emuPerPxAt1080), Y: int64(px * emuPerPxAt1080),
			Width: int64(960 * emuPerPxAt1080), Height: int64(120 * emuPerPxAt1080),
		}
	}
	elements := []render.Element{
		{ID: "title", Kind: animation.KindTitle, Bounds: page(54)},
		// 有几何没轨道：底板照旧把它烘在底图里，不会单独成层。
		{ID: "card", Kind: animation.KindShape, Bounds: page(300)},
		// 有轨道没几何：编译器报 anchor_missing 并跳过，同样不成层。
		{ID: "ghost", Kind: animation.KindBullet},
		// 有轨道、有框，但框是零面积：toBox 也按 anchor_missing 拒掉。
		{ID: "flat", Kind: animation.KindShape, Bounds: &render.EMUBox{X: 100, Y: 100, Width: 0, Height: 0}},
		// 整块落在画布右边：钳制后宽度为负，同样不成层（「在不在画布内」要宽度才判得出）。
		{ID: "offright", Kind: animation.KindShape, Bounds: &render.EMUBox{
			X: int64(2100 * emuPerPxAt1080), Y: int64(100 * emuPerPxAt1080),
			Width: int64(200 * emuPerPxAt1080), Height: int64(100 * emuPerPxAt1080),
		}},
	}
	clip := []*animation.Clip{{ID: "c1", Type: "fadeIn", StartMs: 0, DurationMs: 500,
		Easing: "easeOut", Trigger: "onEnter"}}
	rev := &animation.Revision{PresetID: "business-calm", Intensity: 1,
		Tracks: []*animation.Track{
			{ElementID: "title", Kind: animation.KindTitle, Clips: clip},
			{ElementID: "ghost", Kind: animation.KindBullet, Clips: clip},
			{ElementID: "flat", Kind: animation.KindShape, Clips: clip},
			{ElementID: "offright", Kind: animation.KindShape, Clips: clip},
			{ElementID: "", Kind: animation.KindPage, Clips: clip},
		}}

	compiler, err := render.NewCompiler(render.Options{
		Canvas:         design.Canvas{Width: 1920, Height: 1080},
		SlideWidthEMU:  12192000,
		SlideHeightEMU: 6858000,
		FPS:            30,
	})
	if err != nil {
		t.Fatalf("compiler: %v", err)
	}
	out, diags, err := compiler.CompilePage(render.PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 6000, BaseImage: "pages/p0.png",
		Elements: elements, Revision: rev,
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	sawAnchorMissing := false
	for _, d := range diags {
		if d.Code == render.DiagAnchorMissing {
			sawAnchorMissing = true
		}
	}
	if !sawAnchorMissing {
		t.Fatalf("ghost 有轨道没几何，编译器必须报 anchor_missing；got %+v", diags)
	}

	fromPlan := make(map[string]struct{}, len(out.Layers))
	for _, l := range out.Layers {
		if l.ElementID != "" {
			fromPlan[l.ElementID] = struct{}{}
		}
	}
	got := layerElementIDs(rev, elements, canvasAt(1080))

	// 先钉住"该出谁"：只该有 title。两边一起错也不会被这条放过。
	if _, ok := fromPlan["title"]; !ok || len(fromPlan) != 1 {
		t.Fatalf("编译器出的层 = %v，want 只有 title", fromPlan)
	}
	if len(got) != len(fromPlan) {
		t.Fatalf("焦点侧认 %v，编译器出 %v", got, fromPlan)
	}
	for id := range fromPlan {
		if _, ok := got[id]; !ok {
			t.Fatalf("编译器出了 %q 这一层，焦点侧却不认：%v", id, got)
		}
	}
}

// TestFocusReleasesWhenNextTargetMissing：下一段「声明了焦点」但那个目标在解析产物里
// 不存在时，本段必须在句末放手，而不是一直指下去。
//
// 规则写的是「下一段也指东西时不给终点」——判据此前用的是 `next.Focus != nil`，也
// 就是**声明**；可声明了不等于画面上真会亮：目标缺失时那一段被跳过、只留一条诊断。
// 于是上一段的 EndMs 被填成 0（保持到下一段），而下一段并没有东西来接管——遮罩就
// 一直压在已经讲完的对象上，有时直到页末。旁白这时在讲别的事，画面上却还在指旧目标。
// 这正是规则注释里点名要避免的那种错，只是从「目标缺失」这个口子进来。
func TestFocusReleasesWhenNextTargetMissing(t *testing.T) {
	boxes := []FocusBox{
		{ShapeID: "shape-a", Box: box(100, 100, 400, 300)},
		{ShapeID: "shape-b", Box: box(600, 100, 400, 300)},
	}
	endOf := func(spans []render.FocusSpan, id string) (int64, bool) {
		for _, s := range spans {
			if s.ElementID == id {
				return s.EndMs, true
			}
		}
		return 0, false
	}

	// ① 末段的目标缺失：旧行为把上一段的终点填成 0，遮罩一直压到页末。
	slide := slideCue(
		cue(0, 1000, &media.SegmentFocus{ShapeID: "shape-a"}),
		cue(1000, 2000, &media.SegmentFocus{ShapeID: "shape-ghost"}),
	)
	spans, diags := buildFocusSpans(focusCues(slide), boxes, nil, 3000, canvasAt(1080))
	if len(diags) != 1 || diags[0].Code != render.DiagFocusTargetMissing {
		t.Fatalf("缺失目标必须留一条 %s，got %+v", render.DiagFocusTargetMissing, diags)
	}
	end, ok := endOf(spans, "shape-a")
	if !ok {
		t.Fatalf("shape-a 的焦点不该被丢掉：%+v", spans)
	}
	if end != 1000 {
		t.Fatalf("下一段的目标不存在时，shape-a 应在句末(1000ms)放手，got EndMs=%d —— 否则遮罩一直指着已经讲完的对象", end)
	}

	// ② 中间段缺失、后面还有一段：同样在句末放手，交给真正亮起来的那一段。
	slide = slideCue(
		cue(0, 1000, &media.SegmentFocus{ShapeID: "shape-a"}),
		cue(1000, 2000, &media.SegmentFocus{ShapeID: "shape-ghost"}),
		cue(2000, 3000, &media.SegmentFocus{ShapeID: "shape-b"}),
	)
	// 页在 3000ms 结束（最后一段正好讲完）：最后一段的句末就在页末，等于「保持到页末」。
	spans, _ = buildFocusSpans(focusCues(slide), boxes, nil, 3000, canvasAt(1080))
	if end, _ := endOf(spans, "shape-a"); end != 1000 {
		t.Fatalf("shape-a 的终点 = %d，want 1000", end)
	}
	if end, _ := endOf(spans, "shape-b"); end != 0 {
		t.Fatalf("最后一段讲完就是页末，EndMs 应为 0（保持到页末），got %d", end)
	}

	// ③ 下一段指的还是同一个对象（被「焦点互斥」去重）：这里必须保持，不能放手——
	//    同一件事连着讲两句，中间闪一下全亮才是错的。修法不能把这一条一起改坏。
	slide = slideCue(
		cue(0, 1000, &media.SegmentFocus{ShapeID: "shape-a"}),
		cue(1000, 2000, &media.SegmentFocus{ShapeID: "shape-a"}),
	)
	spans, diags = buildFocusSpans(focusCues(slide), boxes, nil, 3000, canvasAt(1080))
	if len(diags) != 1 || diags[0].Code != render.DiagFocusDuplicate {
		t.Fatalf("重复目标必须留一条 %s，got %+v", render.DiagFocusDuplicate, diags)
	}
	if end, _ := endOf(spans, "shape-a"); end != 0 {
		t.Fatalf("下一段讲的是同一个对象，应保持到页末（EndMs=0），got %d", end)
	}
}
