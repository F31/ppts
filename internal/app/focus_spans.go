package app

import (
	"fmt"
	"math"
	"sort"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/design"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/render"
)

// 本文件把「讲稿说这段讲谁」翻译成「画面上这一刻留哪块亮」。
//
// 三段：
//  1. 时间轴给出每段旁白的起点/终点（它是唯一的时钟），焦点跟着分段走；
//  2. 解析产物给出目标在哪（形状的 EMU 框、每根柱子的框）；
//  3. 两边一合，就是 render.FocusSpan——带 Rect 的走矩形遮罩（柱子没有图层），
//     不带 Rect 的走图层压暗（形状本来就是元素）。
//
// 为什么不在渲染侧按 target 现查：渲染服务拿不到 document.json，也不该为了一个
// 焦点去读对象存储。焦点是内容层的事，在这里定死成矩形，渲染只要照着盖白。

// focusCue 是一段旁白在页内的时间与目标。Focus 可空：空表示「这段不指」，
// 它的存在只影响前一段要不要在句末放手（见 buildFocusSpans）。
type focusCue struct {
	SegmentID string
	StartMs   int64
	EndMs     int64
	Focus     *media.SegmentFocus
}

// focusCues 把时间轴的一页摊成页内毫秒。时间轴是全局时钟（含 lead-in 与页间间隔），
// 焦点要的是「相对本页起点多少毫秒」——两者不是一个钟，混用会让焦点整体晚一页前导。
func focusCues(slide media.SlideCue) []focusCue {
	out := make([]focusCue, 0, len(slide.Segments))
	for _, seg := range slide.Segments {
		start := (seg.StartUS - slide.StartUS) / 1000
		end := (seg.EndUS - slide.StartUS) / 1000
		out = append(out, focusCue{SegmentID: seg.SegmentID, StartMs: start, EndMs: end, Focus: seg.Focus})
	}
	return out
}

// buildFocusSpans 生成一页的焦点序列。
//
// 三条规则，都不是猜的：
//
//   - 起点 = 这段旁白开口的那一刻。提前指等于"还没讲到就先灰一片"，延后又对不上口型。
//   - 终点：**下一段也指东西时不给终点**（保持到下一段开始），否则在句末放手。
//     句间通常只有 200–300ms 的停顿，每句都放手会让整页每隔一句闪一下全亮；
//     而下一段改讲别的事时不放手，画面就一直指着一个已经讲完的对象。两条都是错的，
//     区别在于段与段之间到底有没有换话题——"下一段有没有焦点"正好是这个信息。
//   - 目标不存在就跳过这一段并留诊断，而不是让整页编译失败。焦点是增强层：
//     一份目标写错的稿子应该照样能出片（那一页全亮），而不是导不出视频。
func buildFocusSpans(cues []focusCue, boxes []FocusBox, layerIDs map[string]struct{},
	pageMs int64, canvas design.Canvas) ([]render.FocusSpan, []render.Diagnostic) {
	if pageMs <= 0 || len(cues) == 0 || canvas.Width <= 0 || canvas.Height <= 0 {
		return nil, nil
	}
	shapeBoxes := make(map[string]FocusBox, len(boxes))
	barBoxes := make(map[string]FocusBox, len(boxes))
	for _, b := range boxes {
		if b.BarID == "" {
			if _, ok := shapeBoxes[b.ShapeID]; !ok {
				shapeBoxes[b.ShapeID] = b
			}
			continue
		}
		barBoxes[barKey(b.ShapeID, b.BarID)] = b
	}

	// outcomes 与 cues 一一对应：记下每段**声明**了什么目标、以及它到底有没有**产出**
	// 一个焦点。两件事必须分开——下一段声明了目标却找不到时，画面上什么都不会亮，
	// 这时「下一段也指东西」的前提不成立（见 focusEnd）。
	outcomes := make([]focusOutcome, len(cues))
	var spans []render.FocusSpan
	var diags []render.Diagnostic
	seen := make(map[string]struct{}, len(cues))
	for i, cue := range cues {
		f := cue.Focus
		if f == nil || f.ShapeID == "" {
			continue
		}
		key := barKey(f.ShapeID, f.BarID)
		outcomes[i].key = key
		if _, dup := seen[key]; dup {
			diags = append(diags, render.Diagnostic{
				Code: render.DiagFocusDuplicate, ElementID: key,
				Message: fmt.Sprintf("%s 又被 %s 指向；本页焦点互斥，只保留第一次", f.ShapeID, cue.SegmentID),
			})
			continue
		}
		span, diag, ok := focusSpanFor(cue, f, shapeBoxes, barBoxes, layerIDs, pageMs, canvas)
		if !ok {
			diags = append(diags, diag)
			continue
		}
		seen[key] = struct{}{}
		outcomes[i].resolved = true
		outcomes[i].span = len(spans)
		spans = append(spans, span)
	}
	// 终点在全部解析完之后再定：它取决于**下一段有没有真的亮起来**，而不是它声明了什么。
	for i := range outcomes {
		if !outcomes[i].resolved {
			continue
		}
		spans[outcomes[i].span].EndMs = focusEnd(i, cues, outcomes, pageMs)
	}
	sort.SliceStable(spans, func(a, b int) bool { return spans[a].StartMs < spans[b].StartMs })
	return spans, diags
}

// focusOutcome 是一段旁白在焦点侧的结局。
//
// key 是它**声明**的目标（空表示这段不指），resolved 表示这个目标真的产出了焦点。
// 两者不等只有一种情况：目标在解析产物里找不到、起点不在本页内、或被互斥去重。
type focusOutcome struct {
	key      string
	resolved bool
	span     int // resolved 时对应的 spans 下标（排序前）
}

func barKey(shapeID, barID string) string {
	if barID == "" {
		return shapeID
	}
	return shapeID + "#" + barID
}

// layerElementIDs 返回「这一版规格里真会被切成图层的元素」。
//
// 判据本身在 render.LayerElementIDs——那是**关于渲染器行为的断言**，只能有一份。
// 这里保留一个薄入口，只为让调用处读起来是「焦点要的层集」而不是「渲染包的函数」；
// 不要在这层再加判断：上一个版本就是这么漂的（注释写着「有几何」，实现只判了 ID）。
//
// 跨页延续（G8）不需要额外照顾：它只在已有层上追加延续段，从不替次页造层
// （找不到层就记 continuation_target_missing），所以判据天然覆盖它。
func layerElementIDs(rev *animation.Revision, elements []render.Element, canvas design.Canvas) map[string]struct{} {
	return render.LayerElementIDs(rev, elements, canvas, stdSlideHeightEMU)
}

// PageFocusSpans 算**单页**的焦点序列，数据全部由调用方给。
//
// 预览路径用这个而不是走 AnimationPageAssembler：预览只渲一页，它手里已经有
// project.Page（读页文档时拿到的）与这一页的时间轴 cue，再让组装器按源版本去
// 读一遍整份 document.json 纯属白跑一次对象读——而预览的预算本来就只有几秒。
//
// canvas 是**本页画布**（不是导出画布）：EMU → 像素的系数、以及「框有没有落在画布
// 里」都要跟着这次真正要渲的画布走。预览是 960 宽，导出是 1920 宽，用错会让遮罩
// 缩成错误的尺寸，画面上表现成"孔开在旁边"，而日志一切正常。
//
// rev 决定「哪个形状能靠压暗图层来指」：与导出侧用同一个 layerElementIDs（也就是
// 同一个 render.LayerElementIDs），两条路对同一个目标必须给出同一种表达，否则预览
// 在压暗、成片在盖白。
func PageFocusSpans(page *project.Page, slide media.SlideCue, rev *animation.Revision,
	pageMs int64, canvas design.Canvas) ([]render.FocusSpan, []render.Diagnostic) {
	if page == nil || pageMs <= 0 || canvas.Width <= 0 || canvas.Height <= 0 {
		return nil, nil
	}
	hasFocus := false
	for _, seg := range slide.Segments {
		if seg.Focus != nil && seg.Focus.ShapeID != "" {
			hasFocus = true
			break
		}
	}
	// 绝大多数页一段都没有焦点。先在时间轴上判完再动形状树与元素换算：
	// 预览每点一次都要付这份成本，而它十次里有九次用不上。
	if !hasFocus {
		return nil, nil
	}
	return buildFocusSpans(focusCues(slide), PageFocusBoxes(page),
		layerElementIDs(rev, PageElements(page), canvas), pageMs, canvas)
}

// focusSpanFor 解析一个目标，返回它在画布上的表达。
func focusSpanFor(cue focusCue, f *media.SegmentFocus, shapeBoxes, barBoxes map[string]FocusBox,
	layerIDs map[string]struct{}, pageMs int64, canvas design.Canvas) (render.FocusSpan, render.Diagnostic, bool) {
	span := render.FocusSpan{ElementID: barKey(f.ShapeID, f.BarID)}
	if cue.StartMs < 0 || cue.StartMs >= pageMs {
		return span, render.Diagnostic{
			Code: render.DiagFocusTargetMissing, ElementID: span.ElementID,
			Message: fmt.Sprintf("%s 的起点 %dms 不在本页 [0,%dms) 内，本段不指", cue.SegmentID, cue.StartMs, pageMs),
		}, false
	}

	if f.BarID != "" {
		b, ok := barBoxes[barKey(f.ShapeID, f.BarID)]
		if !ok {
			return span, render.Diagnostic{
				Code: render.DiagFocusTargetMissing, ElementID: span.ElementID,
				Message: fmt.Sprintf("%s 指向 %s，但解析产物里没有这根柱子", cue.SegmentID, span.ElementID),
			}, false
		}
		rect, why := toCanvasRect(b.Box, canvas)
		if rect == nil {
			return span, render.Diagnostic{
				Code: render.DiagFocusTargetMissing, ElementID: span.ElementID,
				Message: fmt.Sprintf("%s 的框%s，本段不指", span.ElementID, why),
			}, false
		}
		span.Rect = rect
		span.StartMs = cue.StartMs
		return span, render.Diagnostic{}, true
	}

	// 整个形状：本来就是图层时走压暗（渲染器的既有语言），否则退到矩形遮罩。
	if _, isLayer := layerIDs[f.ShapeID]; isLayer {
		span.StartMs = cue.StartMs
		return span, render.Diagnostic{}, true
	}
	b, ok := shapeBoxes[f.ShapeID]
	if !ok {
		return span, render.Diagnostic{
			Code: render.DiagFocusTargetMissing, ElementID: span.ElementID,
			Message: fmt.Sprintf("%s 指向 %s，但这一页没有这个形状的包围盒", cue.SegmentID, f.ShapeID),
		}, false
	}
	rect, why := toCanvasRect(b.Box, canvas)
	if rect == nil {
		return span, render.Diagnostic{
			Code: render.DiagFocusTargetMissing, ElementID: span.ElementID,
			Message: fmt.Sprintf("%s 的框%s，本段不指", f.ShapeID, why),
		}, false
	}
	span.Rect = rect
	span.StartMs = cue.StartMs
	return span, render.Diagnostic{}, true
}

// focusEnd 决定这一段要不要在句末放手。
//
// 下一段**真的会亮出焦点** → 0（保持，交给下一段接管）；下一段讲的就是同一个对象
// （被「焦点互斥」去重）→ 也保持，同一件事连着讲两句，中间闪一下全亮才是错的；
// 其余情况（下一段不指、或者指了但那个目标不存在）→ 句末放手。
//
// 「存在」与「声明」在这里必须分开判：目标缺失时那一段会被跳过、只留一条诊断，
// 画面上没有任何东西来接管。若照 `next.Focus != nil` 就保持，遮罩会一直压在已经
// 讲完的对象上——旁白已经在讲别的事，画面还在指旧目标，有时一直压到页末。
//
// 算出来的终点落在页末之后或不在本段内就不给（"放手"变成一个不存在的时间点，
// 等于一直指到结尾，与不给终点等价但更难看懂）。
func focusEnd(i int, cues []focusCue, outcomes []focusOutcome, pageMs int64) int64 {
	if i+1 < len(cues) {
		next := outcomes[i+1]
		if next.resolved || (next.key != "" && next.key == outcomes[i].key) {
			return 0
		}
	}
	end := cues[i].EndMs
	if end <= cues[i].StartMs || end >= pageMs {
		return 0
	}
	return end
}

// pxToEMU 把解析画布像素（1920×1080）换算成 EMU。
//
// 与切柱时的换算互为逆运算：那边用 pxPerEMU = 页高像素 / 6858000，这边用
// bindCanvasH / 6858000。写在一处而不是两处各写一遍，是为了让"框的单位"这件事
// 只有一个地方能说错。
func pxToEMU(px int) int64 {
	return int64(math.Round(float64(px) * stdSlideHeightEMU / bindCanvasH))
}

// toCanvasRect 把 EMU 框换算成画布像素；返回 nil 时 why 说明这块区域为什么指不了。
//
// 画布宽高必须参与：只知道高度时判不出「这个框整个落在画布右边」，而那种元素的遮罩
// 会把孔开到画面外——合成器把它钳在边缘，看起来就是一块盖住左上角的白纱。渲染器
// 对同一个框的做法是钳制后无面积就不成层（boxOf），这里跟着它判。
func toCanvasRect(b render.EMUBox, canvas design.Canvas) (*render.Box, string) {
	emuToPx := float64(canvas.Height) / float64(stdSlideHeightEMU)
	rect := &render.Box{
		X: int(math.Round(float64(b.X) * emuToPx)),
		Y: int(math.Round(float64(b.Y) * emuToPx)),
		W: int(math.Round(float64(b.Width) * emuToPx)),
		H: int(math.Round(float64(b.Height) * emuToPx)),
	}
	if rect.W <= 0 || rect.H <= 0 {
		return nil, fmt.Sprintf("换算后为空（%d×%d）", b.Width, b.Height)
	}
	// 判交集要用换算后、**未钳制**的原点：钳到 0 之后就永远判不出「在画布右边」。
	if rect.X >= canvas.Width || rect.Y >= canvas.Height {
		return nil, fmt.Sprintf("整个落在画布外（原点 %d,%d，画布 %d×%d）",
			rect.X, rect.Y, canvas.Width, canvas.Height)
	}
	// 负的原点会让合成器画出一块看不见的孔，而报告里一切正常（validateFocus 也会拒）。
	if rect.X < 0 {
		rect.X = 0
	}
	if rect.Y < 0 {
		rect.Y = 0
	}
	return rect, ""
}

// focusSource 是组装器要的窄能力：按源版本取「可作焦点的框」。
// 单独成接口是为了让测试不必造假对象存储。
type focusSource = PageFocusSource
