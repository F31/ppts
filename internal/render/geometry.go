package render

import (
	"fmt"
	"math"
)

// 本文件只放几何换算：源页坐标（EMU）→ 画布像素，以及越界钳制。
// 换算规则必须只有一份——内容层（LayerElementIDs）与编译层都要问"这个元素会不会真成层"，
// 两份实现会让两边对同一个元素给出不同答案，而画面逐像素不变、诊断全绿。

// EMUBox 是源页坐标下的轴对齐矩形（EMU）。
//
// 刻意不复用 project.Box：那会把 internal/project 连同 internal/db（sqlite 驱动）
// 一起链接进渲染服务二进制。渲染镜像不需要数据库，也不该背它。
// 调用方做一次 3 个字段的拷贝，换来 L4 的干净依赖面。
type EMUBox struct {
	X, Y, Width, Height int64
}

// boxOf 是 toBox 的纯计算部分：EMU 框换算成画布像素框，并钳制到画布内。
//
// 抽成包级函数是为了让**内容层也能问同一个问题**（见 LayerElementIDs）：焦点要决定
// 「压暗图层还是退成矩形」，而它必须知道这一版会不会真的为某个元素出层。这份换算
// 规则一旦有两份实现，两边迟早对同一个元素给出不同答案——而答案不同的表现是
// 画面逐像素不变、诊断全绿（7053433 / f382f89 都是这个病）。
//
// ok=false 时 why 说明原因；clamped 表示框越界被裁过（不影响是否成框）。
func boxOf(b *EMUBox, scale float64, canvasW, canvasH int) (box Box, clamped, ok bool, why string) {
	if b == nil || b.Width <= 0 || b.Height <= 0 {
		return Box{}, false, false, "元素没有可用包围盒（无几何），元素级动画无法挂载"
	}
	x := int(math.Round(float64(b.X) * scale))
	y := int(math.Round(float64(b.Y) * scale))
	w := int(math.Round(float64(b.Width) * scale))
	h := int(math.Round(float64(b.Height) * scale))
	if x < 0 {
		w += x
		x = 0
		clamped = true
	}
	if y < 0 {
		h += y
		y = 0
		clamped = true
	}
	if x+w > canvasW {
		w = canvasW - x
		clamped = true
	}
	if y+h > canvasH {
		h = canvasH - y
		clamped = true
	}
	box = Box{X: x, Y: y, W: w, H: h}
	// 完全落在画布右侧/下方之外时，钳制后宽或高会变成 ≤0：这种元素在成片里看不见，
	// 也不该占一个图层。
	if w <= 0 || h <= 0 {
		return box, clamped, false, "包围盒换算后无面积，元素级动画无法挂载"
	}
	return box, clamped, true, ""
}

// toBox 把 EMU 包围盒换算为像素并钳制到画布内。
func (c *Compiler) toBox(b *EMUBox, page int, elementID string) (Box, bool, *Diagnostic) {
	box, clamped, ok, why := boxOf(b, c.scale, c.opt.Canvas.Width, c.opt.Canvas.Height)
	if !ok {
		return box, false, &Diagnostic{
			Code: DiagAnchorMissing, Page: page, ElementID: elementID, Message: why,
		}
	}
	var diag *Diagnostic
	if clamped {
		diag = &Diagnostic{
			Code: DiagBoxClamped, Page: page, ElementID: elementID,
			Message: fmt.Sprintf("包围盒越界，已钳制为 (%d,%d %dx%d)", box.X, box.Y, box.W, box.H),
		}
	}
	return box, true, diag
}

// GazeRectPadPx 是注视矩形的外扩（页坐标像素）。夹到画布之后才写进 Focus.rect。
const GazeRectPadPx = 8

// gazeMaxFrameRatio 是「卡片底」相对页面积的上限。再大就是分区/整页底，不能当圈选。
// 与 bind.cluster 的 0.4 同一条线：六宫格卡片约 7%，两列分区常超过 40%。
const gazeMaxFrameRatio = 0.40

// gazeCoverRatio 是「容器包住了这个元素」的面积门槛。PPT 标题盒会略伸出卡片描边，
// 要求 100% 包含会扩不上去，圈选就停在标题上。
const gazeCoverRatio = 0.80

// gazeRectNearPx 是「同一张卡」的像素容差：扩到同一容器后坐标可能差 1–2px。
const gazeRectNearPx = 4

// PadGazeRect 把焦点盒外扩 pad 并夹到画布内。输入无面积时原样返回。
func PadGazeRect(b Box, canvasW, canvasH int) Box {
	if b.W <= 0 || b.H <= 0 || canvasW <= 0 || canvasH <= 0 {
		return b
	}
	x := b.X - GazeRectPadPx
	y := b.Y - GazeRectPadPx
	w := b.W + GazeRectPadPx*2
	h := b.H + GazeRectPadPx*2
	if x < 0 {
		w += x
		x = 0
	}
	if y < 0 {
		h += y
		y = 0
	}
	if x+w > canvasW {
		w = canvasW - x
	}
	if y+h > canvasH {
		h = canvasH - y
	}
	if w <= 0 || h <= 0 {
		return b
	}
	return Box{X: x, Y: y, W: w, H: h}
}

func boxArea(b Box) float64 {
	if b.W <= 0 || b.H <= 0 {
		return 0
	}
	return float64(b.W) * float64(b.H)
}

func boxIntersection(a, b Box) Box {
	x1 := a.X
	if b.X > x1 {
		x1 = b.X
	}
	y1 := a.Y
	if b.Y > y1 {
		y1 = b.Y
	}
	x2 := a.X + a.W
	if b.X+b.W < x2 {
		x2 = b.X + b.W
	}
	y2 := a.Y + a.H
	if b.Y+b.H < y2 {
		y2 = b.Y + b.H
	}
	if x2 <= x1 || y2 <= y1 {
		return Box{}
	}
	return Box{X: x1, Y: y1, W: x2 - x1, H: y2 - y1}
}

func boxCovers(outer, inner Box) bool {
	innerA := boxArea(inner)
	if innerA <= 0 {
		return false
	}
	return boxArea(boxIntersection(outer, inner))/innerA >= gazeCoverRatio
}

func rectNear(a, b Box) bool {
	dx := a.X - b.X
	if dx < 0 {
		dx = -dx
	}
	dy := a.Y - b.Y
	if dy < 0 {
		dy = -dy
	}
	dw := a.W - b.W
	if dw < 0 {
		dw = -dw
	}
	dh := a.H - b.H
	if dh < 0 {
		dh = -dh
	}
	return dx <= gazeRectNearPx && dy <= gazeRectNearPx && dw <= gazeRectNearPx && dh <= gazeRectNearPx
}

// expandGazeRect 把标题/编号的小盒扩到最小的合法卡片底。没有合适容器时原样返回。
func expandGazeRect(box Box, frames []Box, pageArea float64) Box {
	elArea := boxArea(box)
	if elArea <= 0 {
		return box
	}
	best := box
	bestArea := 0.0
	found := false
	for _, f := range frames {
		fa := boxArea(f)
		if fa <= elArea {
			continue
		}
		if pageArea > 0 && fa/pageArea >= gazeMaxFrameRatio {
			continue
		}
		if !boxCovers(f, box) {
			continue
		}
		if !found || fa < bestArea {
			best = f
			bestArea = fa
			found = true
		}
	}
	if !found {
		return box
	}
	return best
}
