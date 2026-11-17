package render

import (
	"github.com/F31/ppts/internal/animation"
)

// maxBarGrowLayers 是一页合成生长层的上限。超过这个数 Chrome 要挖的孔太多，
// 16 根已经覆盖常见柱状图；多出来的柱子留在底板上，从一开始就在。
const maxBarGrowLayers = 16

func barGrowDurationMs() int64 {
	if eff, ok := animation.Catalog().Effect("grow"); ok && eff.DefaultDuration > 0 {
		return eff.DefaultDuration
	}
	return 900
}

// synthesizeBarGrowLayers 给带 ChartBars 几何的柱子补 grow 层。
//
// 规格里的整图 grow 在 gaze+entrance 会被 compileTrack 跳过（整图挖洞会把坐标轴
// 一起抹掉）。逐柱生长走这条合成路径：盒是柱体（不含标签带），起点跟拍表 ShapeID，
// 对不上就在页内均分。调用方必须已经确认本页会挖洞（!skipEntrance）。
func (c *Compiler) synthesizeBarGrowLayers(in PageInput, existing []Layer, intensity float64) []Layer {
	have := make(map[string]struct{}, len(existing))
	for _, l := range existing {
		if l.ElementID != "" {
			have[l.ElementID] = struct{}{}
		}
	}
	bars := make([]Element, 0, 8)
	for _, el := range in.Elements {
		if el.Kind != animation.KindBar || el.ID == "" || el.Bounds == nil {
			continue
		}
		if _, dup := have[el.ID]; dup {
			continue
		}
		bars = append(bars, el)
		if len(bars) >= maxBarGrowLayers {
			break
		}
	}
	if len(bars) == 0 {
		return nil
	}
	growDur := barGrowDurationMs()
	starts := barGrowStarts(bars, in.BeatSheet, in.DurationMs, growDur)
	frame := frameMS(c.opt.FPS)
	out := make([]Layer, 0, len(bars))
	for i, el := range bars {
		layer := Layer{
			ElementID: el.ID,
			Kind:      animation.KindBar,
			AssetMode: AssetBitmapRegion,
		}
		box, ok, _ := c.toBox(el.Bounds, in.Index, el.ID)
		if !ok {
			continue
		}
		layer.Box = box
		axis := el.GrowAxis
		if axis == "" {
			axis = "y"
		}
		clip := &animation.Clip{
			ID:         "grow-" + el.ID,
			Type:       "grow",
			StartMs:    starts[i],
			DurationMs: growDur,
			Easing:     "easeOut",
			Trigger:    animation.TriggerEnter,
			Params:     animation.Params{Axis: axis},
		}
		m, _, ok := c.compileClip(in, clip, layer, intensity, frame, el.ID)
		if !ok {
			continue
		}
		layer.Clips = []Motion{m}
		if el.GrowOrigin != "" {
			layer.Origin = el.GrowOrigin
		} else {
			layer.Origin = originFor(layer.Clips)
		}
		out = append(out, layer)
	}
	return out
}

func barGrowStarts(bars []Element, sheet *animation.BeatSheet, pageDur, growDur int64) []int64 {
	n := len(bars)
	starts := make([]int64, n)
	matched := make([]bool, n)
	nMatch := 0
	if sheet != nil {
		byID := make(map[string]int64, len(sheet.Beats))
		for _, b := range sheet.Beats {
			if b.ShapeID == "" || b.Shot == animation.BeatShotHold {
				continue
			}
			if _, seen := byID[b.ShapeID]; seen {
				continue
			}
			byID[b.ShapeID] = b.StartMs
		}
		for i, el := range bars {
			if t, ok := byID[el.ID]; ok {
				starts[i] = t
				matched[i] = true
				nMatch++
			}
		}
	}
	if nMatch == 0 {
		return equalSpreadStarts(n, pageDur, growDur)
	}
	fallback := equalSpreadStarts(n-nMatch, pageDur, growDur)
	fi := 0
	for i := range starts {
		if matched[i] {
			starts[i] = clampGrowStart(starts[i], pageDur, growDur)
			continue
		}
		starts[i] = fallback[fi]
		fi++
	}
	return starts
}

func equalSpreadStarts(n int, pageDur, growDur int64) []int64 {
	out := make([]int64, n)
	if n <= 1 {
		return out
	}
	usable := pageDur - growDur
	if usable < 0 {
		usable = 0
	}
	for i := 0; i < n; i++ {
		out[i] = usable * int64(i) / int64(n-1)
	}
	return out
}

func clampGrowStart(start, pageDur, growDur int64) int64 {
	if start < 0 {
		start = 0
	}
	maxStart := pageDur - growDur
	if maxStart < 0 {
		maxStart = 0
	}
	if start > maxStart {
		return maxStart
	}
	return start
}
