package render

import (
	"image"
	"image/color"
	"image/draw"
	"sort"
)

// 底板补洞的判定参数。
const (
	// plateRingPx 是采样环的宽度（像素，图像坐标系）。1px 在高分底图上样本太少，
	// 2px 在中位色上更稳，同时仍能贴合元素外沿的底色。
	plateRingPx = 2
	// plateNoiseThreshold 是「这圈颜色不平整」的阈值（每通道 max-min，0–255）。
	// 超过它多半意味着底图是照片/强渐变，中位色补丁会露馅——此时仍然补，
	// 但必须记一条 warning，让下游知道这页的画面可能不干净。
	plateNoiseThreshold = 48
)

// PlatePatch 是一次补洞的留痕。
type PlatePatch struct {
	ElementID string
	Box       Box
	Color     color.NRGBA
	// Noisy 表示采样环的颜色差异超过阈值：补丁可能与周围不连续。
	Noisy bool
	// Spread 是采样环每通道 max-min 的最大值，用于排查「为什么这页出 warning」。
	Spread int
}

// BuildPlate 生成「去掉元素本体的底板」。
//
// 背景：底图是整页烘焙的 PNG。把某个元素区域抬升成独立图层后，原位置仍留着元素本体，
// 入场动画期间会看到「目标位置的静态副本 + 移动副本」（fadeIn 更是完全无效）。
// 补洞就是把这块区域先用周围底色填平，再让图层盖上去（design.md ADR-6）。
//
// 为什么用「盒外环的中位色」而不是均值：均值会被环里的零星深色像素（边框、阴影）
// 拖偏；中位色对少量离群值不敏感，代价是要排序。
//
// canvasW/canvasH 是 IR 的画布尺寸；底图分辨率可能不同（导出放大过），
// 因此盒子要按比例换算到图像坐标再填。
func BuildPlate(base image.Image, layers []Layer, canvasW, canvasH int) (image.Image, []PlatePatch) {
	bounds := base.Bounds()
	plate := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(plate, plate.Bounds(), base, bounds.Min, draw.Src)
	if canvasW <= 0 || canvasH <= 0 {
		return plate, nil
	}
	sx := float64(bounds.Dx()) / float64(canvasW)
	sy := float64(bounds.Dy()) / float64(canvasH)

	var patches []PlatePatch
	for _, layer := range layers {
		// 整页图层（elementId 为空）不需要补洞：它就是底图本身。
		if layer.ElementID == "" || layer.Box.W <= 0 || layer.Box.H <= 0 {
			continue
		}
		rect := image.Rect(
			int(float64(layer.Box.X)*sx),
			int(float64(layer.Box.Y)*sy),
			int(float64(layer.Box.X+layer.Box.W)*sx),
			int(float64(layer.Box.Y+layer.Box.H)*sy),
		).Intersect(plate.Bounds())
		if rect.Empty() {
			continue
		}
		fill, spread, ok := ringMedianColor(plate, rect, plateRingPx)
		if !ok {
			// 元素几乎占满整页，没有外环可采样。跳过补洞而不是拿全图均值——
			// 那会把整页的平均色糊到元素上，比残影更难看。
			continue
		}
		draw.Draw(plate, rect, &image.Uniform{C: fill}, image.Point{}, draw.Src)
		patches = append(patches, PlatePatch{
			ElementID: layer.ElementID,
			Box:       layer.Box,
			Color:     fill,
			Noisy:     spread > plateNoiseThreshold,
			Spread:    spread,
		})
	}
	return plate, patches
}

// ringMedianColor 取 rect 外 ring 像素的逐通道中位色，并返回每通道的最大极差。
func ringMedianColor(img *image.NRGBA, rect image.Rectangle, ring int) (color.NRGBA, int, bool) {
	outer := rect.Inset(-ring).Intersect(img.Bounds())
	var rs, gs, bs []uint8
	for y := outer.Min.Y; y < outer.Max.Y; y++ {
		for x := outer.Min.X; x < outer.Max.X; x++ {
			if x >= rect.Min.X && x < rect.Max.X && y >= rect.Min.Y && y < rect.Max.Y {
				continue // 环只取 rect 之外
			}
			c := img.NRGBAAt(x, y)
			rs = append(rs, c.R)
			gs = append(gs, c.G)
			bs = append(bs, c.B)
		}
	}
	if len(rs) < 8 {
		return color.NRGBA{}, 0, false
	}
	spread := maxSpread(rs, gs, bs)
	return color.NRGBA{
		R: median(rs),
		G: median(gs),
		B: median(bs),
		A: 0xFF,
	}, spread, true
}

func median(values []uint8) uint8 {
	sorted := make([]uint8, len(values))
	copy(sorted, values)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}

func maxSpread(channels ...[]uint8) int {
	spread := 0
	for _, values := range channels {
		if len(values) == 0 {
			continue
		}
		lo, hi := values[0], values[0]
		for _, v := range values[1:] {
			if v < lo {
				lo = v
			}
			if v > hi {
				hi = v
			}
		}
		if d := int(hi) - int(lo); d > spread {
			spread = d
		}
	}
	return spread
}
