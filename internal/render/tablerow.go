package render

import (
	"github.com/F31/ppts/internal/animation"
)

// maxRowFadeLayers 是一页合成行淡入层的上限。超过这个数 look 链挖的孔太多，
// 16 行已经覆盖常见对比表；多出来的行留在底板上，从一开始就在。
const maxRowFadeLayers = 16

func rowFadeDurationMs() int64 {
	if eff, ok := animation.Catalog().Effect("fadeIn"); ok && eff.DefaultDuration > 0 {
		return eff.DefaultDuration
	}
	return 400
}

// synthesizeRowFadeLayers 给带 TableCells 几何的数据行补 fadeIn 层。
//
// 规格里的整表 fadeIn 在 gaze+entrance 会被 compileTrack 跳过。逐行淡入走这条
// 合成路径：盒是整表高按行数均分（表头不挖），起点跟拍表 ShapeID，对不上就在
// 页内均分。fadeIn 留在 look 链上（和 grow 不同）。调用方必须已经确认本页会挖洞。
func (c *Compiler) synthesizeRowFadeLayers(in PageInput, existing []Layer, intensity float64) []Layer {
	have := make(map[string]struct{}, len(existing))
	for _, l := range existing {
		if l.ElementID != "" {
			have[l.ElementID] = struct{}{}
		}
	}
	rows := make([]Element, 0, 8)
	for _, el := range in.Elements {
		if el.Kind != animation.KindRow || el.ID == "" || el.Bounds == nil {
			continue
		}
		if _, dup := have[el.ID]; dup {
			continue
		}
		rows = append(rows, el)
		if len(rows) >= maxRowFadeLayers {
			break
		}
	}
	if len(rows) == 0 {
		return nil
	}
	fadeDur := rowFadeDurationMs()
	starts := barGrowStarts(rows, in.BeatSheet, in.DurationMs, fadeDur)
	frame := frameMS(c.opt.FPS)
	out := make([]Layer, 0, len(rows))
	for i, el := range rows {
		layer := Layer{
			ElementID: el.ID,
			Kind:      animation.KindRow,
			AssetMode: AssetBitmapRegion,
		}
		box, ok, _ := c.toBox(el.Bounds, in.Index, el.ID)
		if !ok {
			continue
		}
		layer.Box = box
		clip := &animation.Clip{
			ID:         "fade-" + el.ID,
			Type:       "fadeIn",
			StartMs:    starts[i],
			DurationMs: fadeDur,
			Easing:     "easeOut",
			Trigger:    animation.TriggerEnter,
		}
		m, _, ok := c.compileClip(in, clip, layer, intensity, frame, el.ID)
		if !ok {
			continue
		}
		layer.Clips = []Motion{m}
		out = append(out, layer)
	}
	return out
}
