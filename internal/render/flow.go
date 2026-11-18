package render

import (
	"fmt"
)

// flowOverlays 把导演 Intent 里的 A→B 传递编译成页级 Overlay。
// 它是渲染层的唯一翻译点：导演只说 from/to/carrier/route，具体坐标和默认样式在这里定。
func flowOverlays(in PageInput, scale float64, canvasW, canvasH int) ([]Overlay, []Diagnostic) {
	if in.BeatSheet == nil || len(in.BeatSheet.Beats) == 0 {
		return nil, nil
	}
	byID := make(map[string]Element, len(in.Elements))
	for _, el := range in.Elements {
		byID[el.ID] = el
	}
	pageArea := float64(canvasW) * float64(canvasH)
	var out []Overlay
	var diags []Diagnostic
	for i, b := range in.BeatSheet.Beats {
		if b.Intent == nil || b.Intent.Flow == nil {
			continue
		}
		f := b.Intent.Flow
		from, ok1 := byID[f.From]
		to, ok2 := byID[f.To]
		if !ok1 || !ok2 {
			diags = append(diags, Diagnostic{
				Code: DiagFlowTargetMissing, Page: in.Index,
				Message: fmt.Sprintf("flow %d: from/to 不在本页元素集合 (%q -> %q)", i, f.From, f.To),
			})
			continue
		}
		fromBox, _, ok1, _ := boxOf(from.Bounds, scale, canvasW, canvasH)
		toBox, _, ok2, _ := boxOf(to.Bounds, scale, canvasW, canvasH)
		if !ok1 || !ok2 {
			diags = append(diags, Diagnostic{
				Code: DiagFlowTargetMissing, Page: in.Index,
				Message: fmt.Sprintf("flow %d: from/to 几何不可用", i),
			})
			continue
		}
		if pageArea > 0 && (boxArea(fromBox)/pageArea >= plateFullPageRatio || boxArea(toBox)/pageArea >= plateFullPageRatio) {
			continue
		}
		start := f.StartMs
		if start <= 0 {
			start = b.StartMs
		}
		if start < 0 {
			start = 0
		}
		duration := f.DurationMs
		if duration < MinOverlayDurationMS {
			duration = 900
		}
		if start+duration > in.DurationMs {
			duration = in.DurationMs - start
		}
		if duration < MinOverlayDurationMS {
			diags = append(diags, Diagnostic{
				Code: DiagMotionBudget, Page: in.Index,
				Message: fmt.Sprintf("flow %d: 页内剩余时间不足 %dms", i, MinOverlayDurationMS),
			})
			continue
		}
		kind := f.Carrier
		switch kind {
		case OverlayDot, OverlayPulse, OverlayArrow, OverlayLine:
		default:
			kind = OverlayDot
		}
		fromX := float64(fromBox.X + fromBox.W/2)
		fromY := float64(fromBox.Y + fromBox.H/2)
		toX := float64(toBox.X + toBox.W/2)
		toY := float64(toBox.Y + toBox.H/2)
		points := []OverlayPoint{{X: fromX, Y: fromY}, {X: toX, Y: toY}}
		if f.Route == "orthogonal" && fromX != toX && fromY != toY {
			points = []OverlayPoint{
				{X: fromX, Y: fromY},
				{X: fromX, Y: toY},
				{X: toX, Y: toY},
			}
		}
		out = append(out, Overlay{
			ID: fmt.Sprintf("flow-%d", i), Kind: kind,
			StartMs: start, DurationMs: duration, Points: points,
			Color: "#FFD166", SizePx: 12, StrokePx: 2, Opacity: 1,
		})
	}
	if len(out) > MaxOverlaysPerPage {
		diags = append(diags, Diagnostic{
			Code: DiagMotionBudget, Page: in.Index,
			Message: fmt.Sprintf("flow 数 %d 超过每页上限 %d，已丢弃多余项", len(out), MaxOverlaysPerPage),
		})
		out = out[:MaxOverlaysPerPage]
	}
	return out, diags
}
