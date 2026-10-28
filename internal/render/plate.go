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
		rect := scaleRect(layer.Box, sx, sy).Intersect(plate.Bounds())
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

// scaleRect 把画布坐标下的 Box 换算到图像坐标。
func scaleRect(b Box, sx, sy float64) image.Rectangle {
	return image.Rect(
		int(float64(b.X)*sx),
		int(float64(b.Y)*sy),
		int(float64(b.X+b.W)*sx),
		int(float64(b.Y+b.H)*sy),
	)
}

// LayerAsset 是一个元素层的独立位图：已按该层 Box 裁切，且不含其它动画元素的像素。
type LayerAsset struct {
	// Index 对应传入 layers 的下标（调用方据此回填 Layer.Image）。
	Index     int
	ElementID string
	Image     *image.NRGBA
	// Erased 记录为出图而抹掉的其它元素 id：排查「这里为什么少一块」时有用。
	Erased []string
}

// BuildLayerAssets 为每个元素层生成「只含自己」的位图（design ADR-9）。
//
// 与 BuildPlate 的分工：底板要抹掉**所有**元素（它当背景），层素材只抹**子元素**。
// 不这么做的后果：元素 A 的 Box 含另一个动画元素 B 时，A 从 baseImage 裁出来的图里
// 就烘着 B，B 会完全跟随 A 的不透明度——编排里给 B 写的 startMs 看起来失效。
//
// 两条边界规则：
//   - 只抹被本层**包住**的元素（O 的矩形完全落在本层矩形内）。若 O 包住本层，它是本层的背景，
//     抹掉等于把自己的画面打穿；同尺寸的两个元素互不抹，否则两边都被抹空。
//   - 因此图层必须按「父在前、子在后」绘制：父层的子元素已被抹掉，若父层画在子层之后，
//     子元素会被整块盖掉。当前编译产物按 track 顺序出层，正是这个顺序。
//
// 取色 MUST 基于原图而非底板：底板是按图层顺序递进填的（卡片先被填成白），
// 拿它给 bullet 取色会得到白色，把卡片打成白条。
//
// 没有外环可采样（元素几乎占满整页）时不抹它：宁可退化成旧行为，也不要用
// 一个错误的填充色制造更大的破坏。
func BuildLayerAssets(base image.Image, layers []Layer, canvasW, canvasH int) []LayerAsset {
	if canvasW <= 0 || canvasH <= 0 || len(layers) == 0 {
		return nil
	}
	bounds := base.Bounds()
	if bounds.Empty() {
		return nil
	}
	src := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(src, src.Bounds(), base, bounds.Min, draw.Src)

	sx := float64(bounds.Dx()) / float64(canvasW)
	sy := float64(bounds.Dy()) / float64(canvasH)

	type target struct {
		index int
		id    string
		rect  image.Rectangle
		fill  color.NRGBA
	}
	targets := make([]target, 0, len(layers))
	for i, layer := range layers {
		if layer.ElementID == "" || layer.Box.W <= 0 || layer.Box.H <= 0 {
			continue
		}
		rect := scaleRect(layer.Box, sx, sy).Intersect(src.Bounds())
		if rect.Empty() {
			continue
		}
		fill, _, ok := ringMedianColor(src, rect, plateRingPx)
		if !ok {
			continue
		}
		targets = append(targets, target{index: i, id: layer.ElementID, rect: rect, fill: fill})
	}
	if len(targets) == 0 {
		return nil
	}

	assets := make([]LayerAsset, 0, len(targets))
	for _, self := range targets {
		frame := image.NewNRGBA(src.Rect)
		draw.Draw(frame, frame.Bounds(), src, src.Bounds().Min, draw.Src)
		var erased []string
		for _, other := range targets {
			if other.index == self.index {
				continue
			}
			// 只抹「被本层包住的子元素」。反过来的情形（本层被更大的元素包住）必须保留：
			// 那是本层的背景，抹了等于把自己的画面打穿。
			// 同尺寸的两个元素互不抹——否则两边都被抹空。
			if !other.rect.In(self.rect) || other.rect == self.rect {
				continue
			}
			draw.Draw(frame, other.rect, &image.Uniform{C: other.fill}, image.Point{}, draw.Src)
			erased = append(erased, other.id)
		}
		crop := image.NewNRGBA(image.Rect(0, 0, self.rect.Dx(), self.rect.Dy()))
		draw.Draw(crop, crop.Bounds(), frame, self.rect.Min, draw.Src)
		assets = append(assets, LayerAsset{
			Index:     self.index,
			ElementID: self.id,
			Image:     crop,
			Erased:    erased,
		})
	}
	return assets
}
