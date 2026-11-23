package render

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"os"
	"path/filepath"

	"github.com/F31/ppts/internal/animation"
)

// 与 shared/motion.ts FOCUS_DIM_OPACITY=0.55 对齐：非焦点区盖白 1-0.55。
const gazePaintVeilAlpha = 0.45

// gazeHoleFeatherPx 与 Remotion/HTML 遮罩 filter:blur(8px) 同宽。
const gazeHoleFeatherPx = 8

var (
	gazeCalloutColor = color.NRGBA{R: 0xff, G: 0xd1, B: 0x66, A: 0xff}
	gazeCalloutPx    = 3
)

// gazePaintOK 是可以绕开 Chrome 的注视页：纱/框从底图（及已 staging 的底板+层图）
// 画出来。切近、overlay、grow/tilt（isGazeLookPage 已排除）仍走 Remotion。
func gazePaintOK(p Page) bool {
	if !isGazeLookPage(p) {
		return false
	}
	if p.LookCrop {
		return false
	}
	if len(p.Overlays) > 0 {
		return false
	}
	if p.BaseImage == "" {
		return false
	}
	return true
}

func writePaintedLookStills(plan *Timeline, dir string, pages []int) error {
	if plan == nil {
		return fmt.Errorf("render: paint looks: nil plan")
	}
	for _, i := range pages {
		if i < 0 || i >= len(plan.Pages) {
			return fmt.Errorf("render: paint looks: page %d out of range", i)
		}
		page := plan.Pages[i]
		windows := lookWindows(page)
		if len(windows) == 0 {
			return fmt.Errorf("render: paint looks: page %d has no look windows", i)
		}
		basePath, err := workPath(dir, page.BaseImage)
		if err != nil {
			return fmt.Errorf("render: paint looks: page %d base: %w", i, err)
		}
		src, err := readImage(basePath)
		if err != nil {
			return fmt.Errorf("render: paint looks: page %d base: %w", i, err)
		}
		src = CropSlack(src, plan.Width, plan.Height)
		base := cloneNRGBA(src)
		plate := base
		if page.PlateImage != "" {
			pp, err := workPath(dir, page.PlateImage)
			if err != nil {
				return fmt.Errorf("render: paint looks: page %d plate: %w", i, err)
			}
			pimg, err := readImage(pp)
			if err != nil {
				return fmt.Errorf("render: paint looks: page %d plate: %w", i, err)
			}
			plate = cloneNRGBA(CropSlack(pimg, plan.Width, plan.Height))
		}
		layerImgs := make([]*image.NRGBA, len(page.Layers))
		for li, layer := range page.Layers {
			if layer.Image == "" {
				continue
			}
			lp, err := workPath(dir, layer.Image)
			if err != nil {
				return fmt.Errorf("render: paint looks: page %d layer: %w", i, err)
			}
			limg, err := readImage(lp)
			if err != nil {
				return fmt.Errorf("render: paint looks: page %d layer: %w", i, err)
			}
			layerImgs[li] = cloneNRGBA(limg)
		}
		for li, w := range windows {
			composed := composeEntranceFrame(plate, page.Layers, layerImgs, w.MidMs)
			img := paintGazeLookOn(composed, base, page, w.MidMs, plan.Width, plan.Height)
			out := lookStillPath(dir, i, li)
			if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
				return fmt.Errorf("render: paint looks: %w", err)
			}
			if err := writePNGFile(out, img); err != nil {
				return fmt.Errorf("render: paint looks: page %d look %d: %w", i, li, err)
			}
		}
	}
	return nil
}

func paintGazeLook(src *image.NRGBA, page Page, tMs int64, canvasW, canvasH int) *image.NRGBA {
	return paintGazeLookOn(src, src, page, tMs, canvasW, canvasH)
}

func composeEntranceFrame(plate *image.NRGBA, layers []Layer, layerImgs []*image.NRGBA, tMs int64) *image.NRGBA {
	dst := cloneNRGBA(plate)
	for i, layer := range layers {
		if i >= len(layerImgs) || layerImgs[i] == nil {
			continue
		}
		op := layerOpacityAt(layer, tMs)
		if op <= 0.01 {
			continue
		}
		overlayLayer(dst, layerImgs[i], layer.Box, op)
	}
	return dst
}

func layerOpacityAt(layer Layer, tMs int64) float64 {
	op := 1.0
	for _, clip := range layer.Clips {
		p := clipProgressAt(clip, tMs)
		for _, prop := range clip.Props {
			if prop.Name != animation.PropOpacity {
				continue
			}
			op *= prop.From + (prop.To-prop.From)*p
		}
	}
	return op
}

func clipProgressAt(clip Motion, tMs int64) float64 {
	if clip.IsContinuation {
		tMs += clip.ContinuationOffsetMs
	}
	if clip.DurationMs <= 0 {
		if tMs >= clip.StartMs {
			return 1
		}
		return 0
	}
	raw := float64(tMs-clip.StartMs) / float64(clip.DurationMs)
	if raw <= 0 {
		return 0
	}
	if raw >= 1 {
		return 1
	}
	return cubicBezierY(clip.Easing, raw)
}

func cubicBezierY(c animation.CubicBezier, x float64) float64 {
	if x <= 0 {
		return 0
	}
	if x >= 1 {
		return 1
	}
	if c.X1 == 0 && c.Y1 == 0 && c.X2 == 0 && c.Y2 == 0 {
		return x
	}
	t := x
	for i := 0; i < 8; i++ {
		s := bezierSlope(t, c.X1, c.X2)
		if math.Abs(s) < 0.001 {
			break
		}
		cur := bezierComp(t, c.X1, c.X2) - x
		if math.Abs(cur) < 1e-7 {
			return bezierComp(t, c.Y1, c.Y2)
		}
		t -= cur / s
		if t < 0 {
			t = 0
		}
		if t > 1 {
			t = 1
		}
	}
	return bezierComp(t, c.Y1, c.Y2)
}

func bezierComp(t, p1, p2 float64) float64 {
	mt := 1 - t
	return 3*mt*mt*t*p1 + 3*mt*t*t*p2 + t*t*t
}

func bezierSlope(t, p1, p2 float64) float64 {
	mt := 1 - t
	return 3*mt*mt*p1 + 6*mt*t*(p2-p1) + 3*t*t*(1-p2)
}

func overlayLayer(dst, src *image.NRGBA, box Box, opacity float64) {
	if opacity <= 0 {
		return
	}
	if opacity > 1 {
		opacity = 1
	}
	sb := src.Bounds()
	db := dst.Bounds()
	for sy := sb.Min.Y; sy < sb.Max.Y; sy++ {
		for sx := sb.Min.X; sx < sb.Max.X; sx++ {
			dx := box.X + sx - sb.Min.X
			dy := box.Y + sy - sb.Min.Y
			if dx < db.Min.X || dx >= db.Max.X || dy < db.Min.Y || dy >= db.Max.Y {
				continue
			}
			si := src.PixOffset(sx, sy)
			sa := float64(src.Pix[si+3]) / 255 * opacity
			if sa <= 0 {
				continue
			}
			di := dst.PixOffset(dx, dy)
			ia := 1 - sa
			dst.Pix[di+0] = uint8(float64(src.Pix[si+0])*sa + float64(dst.Pix[di+0])*ia)
			dst.Pix[di+1] = uint8(float64(src.Pix[si+1])*sa + float64(dst.Pix[di+1])*ia)
			dst.Pix[di+2] = uint8(float64(src.Pix[si+2])*sa + float64(dst.Pix[di+2])*ia)
			outA := sa + float64(dst.Pix[di+3])/255*ia
			if outA > 1 {
				outA = 1
			}
			dst.Pix[di+3] = uint8(outA * 255)
		}
	}
}

func paintGazeLookOn(composed, base *image.NRGBA, page Page, tMs int64, canvasW, canvasH int) *image.NRGBA {
	dst := cloneNRGBA(composed)
	b := dst.Bounds()
	if canvasW <= 0 {
		canvasW = b.Dx()
	}
	if canvasH <= 0 {
		canvasH = b.Dy()
	}
	if base == nil {
		base = composed
	}
	hole, veil, shape := gazeHoleAt(page, tMs)
	overlayWhite(dst, veil)
	if hole != nil {
		punchGazeHole(dst, composed, *hole, shape)
	}
	for _, r := range page.ClearRects {
		copyBox(dst, base, r, canvasW, canvasH)
	}
	if hole != nil {
		strokeCallout(dst, *hole, page, tMs, canvasW, canvasH)
	}
	paintPointers(dst, page, tMs, canvasW, canvasH)
	return dst
}

func gazeHoleAt(page Page, tMs int64) (*Box, float64, string) {
	var cur *FocusSpan
	for i := range page.Focus {
		f := &page.Focus[i]
		if f.Rect == nil || f.Rect.W < 1 || f.Rect.H < 1 {
			continue
		}
		if tMs < f.StartMs {
			continue
		}
		if f.EndMs > 0 && tMs >= f.EndMs {
			continue
		}
		cur = f
		break
	}
	if cur == nil {
		return nil, 0, AnnotationShapeRect
	}
	veil := gazePaintVeilAlpha
	if cur.VeilAlpha > 0 && cur.VeilAlpha <= 1 {
		veil = cur.VeilAlpha
	}
	shape := AnnotationShapeRect
	rect := *cur.Rect
	for _, a := range page.Annotations {
		if a.Kind != AnnotationCallout || a.Rect == nil {
			continue
		}
		if tMs < a.StartMs {
			continue
		}
		if a.EndMs > 0 && tMs >= a.EndMs {
			continue
		}
		if a.Shape != "" {
			shape = a.Shape
		}
		rect = *a.Rect
		break
	}
	return &rect, veil, shape
}

func punchGazeHole(dst, src *image.NRGBA, hole Box, shape string) {
	if src == nil {
		return
	}
	layout := gazeCalloutLayout(hole, shape)
	b := dst.Bounds()
	pad := gazeHoleFeatherPx + 2
	x0 := layout.X - pad
	y0 := layout.Y - pad
	x1 := layout.X + layout.W + pad
	y1 := layout.Y + layout.H + pad
	if x0 < b.Min.X {
		x0 = b.Min.X
	}
	if y0 < b.Min.Y {
		y0 = b.Min.Y
	}
	if x1 > b.Max.X {
		x1 = b.Max.X
	}
	if y1 > b.Max.Y {
		y1 = b.Max.Y
	}
	sb := src.Bounds()
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			d := gazeHoleSDF(float64(x)+0.5, float64(y)+0.5, layout)
			cover := 1 - clamp01(d/float64(gazeHoleFeatherPx))
			if cover <= 0 {
				continue
			}
			if x < sb.Min.X || x >= sb.Max.X || y < sb.Min.Y || y >= sb.Max.Y {
				continue
			}
			di := dst.PixOffset(x, y)
			si := src.PixOffset(x, y)
			ia := 1 - cover
			dst.Pix[di+0] = uint8(float64(src.Pix[si+0])*cover + float64(dst.Pix[di+0])*ia)
			dst.Pix[di+1] = uint8(float64(src.Pix[si+1])*cover + float64(dst.Pix[di+1])*ia)
			dst.Pix[di+2] = uint8(float64(src.Pix[si+2])*cover + float64(dst.Pix[di+2])*ia)
			dst.Pix[di+3] = uint8(float64(src.Pix[si+3])*cover + float64(dst.Pix[di+3])*ia)
		}
	}
}

type gazeLayout struct {
	X, Y, W, H int
	R          float64
	Circle     bool
}

func gazeCalloutLayout(rect Box, shape string) gazeLayout {
	if shape == AnnotationShapeCircle {
		s := rect.W
		if rect.H < s {
			s = rect.H
		}
		return gazeLayout{
			X: rect.X + (rect.W-s)/2, Y: rect.Y + (rect.H-s)/2,
			W: s, H: s, R: float64(s) / 2, Circle: true,
		}
	}
	short := rect.W
	if rect.H < short {
		short = rect.H
	}
	r := math.Min(12, float64(short)*0.08)
	if r > float64(short)/2 {
		r = float64(short) / 2
	}
	if r < 0 {
		r = 0
	}
	return gazeLayout{X: rect.X, Y: rect.Y, W: rect.W, H: rect.H, R: r}
}

func gazeHoleSDF(px, py float64, layout gazeLayout) float64 {
	if layout.Circle {
		cx := float64(layout.X) + float64(layout.W)/2
		cy := float64(layout.Y) + float64(layout.H)/2
		return math.Hypot(px-cx, py-cy) - layout.R
	}
	return sdfRoundRect(px, py, float64(layout.X), float64(layout.Y), float64(layout.W), float64(layout.H), layout.R)
}

func sdfRoundRect(px, py, x, y, w, h, r float64) float64 {
	if r < 0 {
		r = 0
	}
	cx := x + w/2
	cy := y + h/2
	dx := math.Abs(px-cx) - w/2 + r
	dy := math.Abs(py-cy) - h/2 + r
	ax := dx
	if ax < 0 {
		ax = 0
	}
	ay := dy
	if ay < 0 {
		ay = 0
	}
	return math.Min(math.Max(dx, dy), 0) + math.Hypot(ax, ay) - r
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func overlayWhite(img *image.NRGBA, a float64) {
	if a <= 0 {
		return
	}
	if a > 1 {
		a = 1
	}
	ia := 1 - a
	wa := uint32(math.Round(255 * a))
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			i := img.PixOffset(x, y)
			img.Pix[i+0] = uint8(uint32(float64(img.Pix[i+0])*ia) + wa)
			img.Pix[i+1] = uint8(uint32(float64(img.Pix[i+1])*ia) + wa)
			img.Pix[i+2] = uint8(uint32(float64(img.Pix[i+2])*ia) + wa)
		}
	}
}

func copyBox(dst, src *image.NRGBA, box Box, canvasW, canvasH int) {
	r := clipBox(box, canvasW, canvasH)
	if r.W < 1 || r.H < 1 {
		return
	}
	rect := image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H)
	draw.Draw(dst, rect, src, rect.Min, draw.Src)
}

func clipBox(box Box, w, h int) Box {
	if box.X < 0 {
		box.W += box.X
		box.X = 0
	}
	if box.Y < 0 {
		box.H += box.Y
		box.Y = 0
	}
	if box.X+box.W > w {
		box.W = w - box.X
	}
	if box.Y+box.H > h {
		box.H = h - box.Y
	}
	if box.W < 0 {
		box.W = 0
	}
	if box.H < 0 {
		box.H = 0
	}
	return box
}

func strokeCallout(img *image.NRGBA, hole Box, page Page, tMs int64, canvasW, canvasH int) {
	shape := AnnotationShapeRect
	rect := hole
	for _, a := range page.Annotations {
		if a.Kind != AnnotationCallout || a.Rect == nil {
			continue
		}
		if tMs < a.StartMs {
			continue
		}
		if a.EndMs > 0 && tMs >= a.EndMs {
			continue
		}
		shape = a.Shape
		rect = *a.Rect
		break
	}
	r := clipBox(rect, canvasW, canvasH)
	if r.W < 2 || r.H < 2 {
		return
	}
	strokeCalloutLayout(img, gazeCalloutLayout(r, shape), gazeCalloutPx, gazeCalloutColor)
}

func strokeCalloutLayout(img *image.NRGBA, layout gazeLayout, px int, c color.NRGBA) {
	if px < 1 || layout.W < 2 || layout.H < 2 {
		return
	}
	half := float64(px) / 2
	b := img.Bounds()
	pad := px + 2
	x0 := layout.X - pad
	y0 := layout.Y - pad
	x1 := layout.X + layout.W + pad
	y1 := layout.Y + layout.H + pad
	if x0 < b.Min.X {
		x0 = b.Min.X
	}
	if y0 < b.Min.Y {
		y0 = b.Min.Y
	}
	if x1 > b.Max.X {
		x1 = b.Max.X
	}
	if y1 > b.Max.Y {
		y1 = b.Max.Y
	}
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			d := gazeHoleSDF(float64(x)+0.5, float64(y)+0.5, layout)
			cover := 1 - clamp01(math.Abs(d+half)-half)
			if cover <= 0 {
				continue
			}
			blendNRGBA(img, b, x, y, c, cover)
		}
	}
}

func paintPointers(img *image.NRGBA, page Page, tMs int64, canvasW, canvasH int) {
	for _, a := range page.Annotations {
		if a.Kind != AnnotationPointer || a.Rect == nil {
			continue
		}
		if tMs < a.StartMs {
			continue
		}
		if a.EndMs > 0 && tMs >= a.EndMs {
			continue
		}
		fillGazeRect(img, clipBox(*a.Rect, canvasW, canvasH), gazeCalloutColor)
	}
}

func fillGazeRect(img *image.NRGBA, box Box, c color.NRGBA) {
	b := img.Bounds()
	for y := box.Y; y < box.Y+box.H; y++ {
		for x := box.X; x < box.X+box.W; x++ {
			setNRGBA(img, b, x, y, c)
		}
	}
}

func setNRGBA(img *image.NRGBA, b image.Rectangle, x, y int, c color.NRGBA) {
	blendNRGBA(img, b, x, y, c, 1)
}

func blendNRGBA(img *image.NRGBA, b image.Rectangle, x, y int, c color.NRGBA, a float64) {
	if x < b.Min.X || x >= b.Max.X || y < b.Min.Y || y >= b.Max.Y || a <= 0 {
		return
	}
	if a > 1 {
		a = 1
	}
	i := img.PixOffset(x, y)
	ia := 1 - a
	img.Pix[i+0] = uint8(float64(c.R)*a + float64(img.Pix[i+0])*ia)
	img.Pix[i+1] = uint8(float64(c.G)*a + float64(img.Pix[i+1])*ia)
	img.Pix[i+2] = uint8(float64(c.B)*a + float64(img.Pix[i+2])*ia)
	outA := a*float64(c.A)/255 + float64(img.Pix[i+3])/255*ia
	if outA > 1 {
		outA = 1
	}
	img.Pix[i+3] = uint8(outA * 255)
}

func cloneNRGBA(src image.Image) *image.NRGBA {
	bounds := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(dst, dst.Bounds(), src, bounds.Min, draw.Src)
	return dst
}
