package app

import (
	"fmt"
	"math"
	"sort"

	"github.com/F31/ppts/internal/media"
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
	pageMs int64, emuToPx float64) ([]render.FocusSpan, []render.Diagnostic) {
	if pageMs <= 0 || len(cues) == 0 {
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

	var spans []render.FocusSpan
	var diags []render.Diagnostic
	seen := make(map[string]struct{}, len(cues))
	for i, cue := range cues {
		f := cue.Focus
		if f == nil || f.ShapeID == "" {
			continue
		}
		key := barKey(f.ShapeID, f.BarID)
		if _, dup := seen[key]; dup {
			diags = append(diags, render.Diagnostic{
				Code: render.DiagFocusDuplicate, ElementID: key,
				Message: fmt.Sprintf("%s 又被 %s 指向；本页焦点互斥，只保留第一次", f.ShapeID, cue.SegmentID),
			})
			continue
		}
		span, diag, ok := focusSpanFor(cue, f, shapeBoxes, barBoxes, layerIDs, pageMs, emuToPx)
		if !ok {
			diags = append(diags, diag)
			continue
		}
		span.EndMs = focusEnd(i, cues, cue, pageMs)
		seen[key] = struct{}{}
		spans = append(spans, span)
	}
	sort.SliceStable(spans, func(a, b int) bool { return spans[a].StartMs < spans[b].StartMs })
	return spans, diags
}

func barKey(shapeID, barID string) string {
	if barID == "" {
		return shapeID
	}
	return shapeID + "#" + barID
}

// focusSpanFor 解析一个目标，返回它在画布上的表达。
func focusSpanFor(cue focusCue, f *media.SegmentFocus, shapeBoxes, barBoxes map[string]FocusBox,
	layerIDs map[string]struct{}, pageMs int64, emuToPx float64) (render.FocusSpan, render.Diagnostic, bool) {
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
		rect, ok := toCanvasRect(b.Box, emuToPx)
		if !ok {
			return span, render.Diagnostic{
				Code: render.DiagFocusTargetMissing, ElementID: span.ElementID,
				Message: fmt.Sprintf("%s 的框换算后为空（%d×%d），本段不指", span.ElementID, b.Box.Width, b.Box.Height),
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
	rect, ok := toCanvasRect(b.Box, emuToPx)
	if !ok {
		return span, render.Diagnostic{
			Code: render.DiagFocusTargetMissing, ElementID: span.ElementID,
			Message: fmt.Sprintf("%s 的框换算后为空（%d×%d），本段不指", f.ShapeID, b.Box.Width, b.Box.Height),
		}, false
	}
	span.Rect = rect
	span.StartMs = cue.StartMs
	return span, render.Diagnostic{}, true
}

// focusEnd 决定这一段要不要在句末放手。
//
// 下一段（时间上的下一段，不管它指不指）有焦点 → 0（保持，交给下一段接管）；
// 下一段没有焦点或本段是最后一段 → 句末放手；算出来的终点落在页末之后就不给
// （"放手"变成一个不存在的时间点，等于一直指到结尾，与不给终点等价但更难看懂）。
func focusEnd(i int, cues []focusCue, cue focusCue, pageMs int64) int64 {
	last := i+1 == len(cues)
	if !last {
		next := cues[i+1]
		if next.Focus != nil && next.Focus.ShapeID != "" {
			return 0
		}
	}
	end := cue.EndMs
	if end <= cue.StartMs || end >= pageMs {
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

// toCanvasRect 把 EMU 框换算成画布像素。
func toCanvasRect(b render.EMUBox, emuToPx float64) (*render.Box, bool) {
	rect := &render.Box{
		X: int(math.Round(float64(b.X) * emuToPx)),
		Y: int(math.Round(float64(b.Y) * emuToPx)),
		W: int(math.Round(float64(b.Width) * emuToPx)),
		H: int(math.Round(float64(b.Height) * emuToPx)),
	}
	if rect.W <= 0 || rect.H <= 0 {
		return nil, false
	}
	// 负的原点会让合成器画出一块看不见的孔，而报告里一切正常（validateFocus 也会拒）。
	if rect.X < 0 {
		rect.X = 0
	}
	if rect.Y < 0 {
		rect.Y = 0
	}
	return rect, true
}

// focusSource 是组装器要的窄能力：按源版本取「可作焦点的框」。
// 单独成接口是为了让测试不必造假对象存储。
type focusSource = PageFocusSource
