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
	hole, veil := gazeHoleAt(page, tMs)
	overlayWhite(dst, veil)
	if hole != nil {
		copyBox(dst, composed, *hole, canvasW, canvasH)
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

func gazeHoleAt(page Page, tMs int64) (*Box, float64) {
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
		return nil, 0
	}
	veil := gazePaintVeilAlpha
	if cur.VeilAlpha > 0 && cur.VeilAlpha <= 1 {
		veil = cur.VeilAlpha
	}
	r := *cur.Rect
	return &r, veil
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
	if shape == AnnotationShapeCircle {
		strokeCircle(img, r)
		return
	}
	strokeRect(img, r, gazeCalloutPx, gazeCalloutColor)
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

func strokeRect(img *image.NRGBA, box Box, px int, c color.NRGBA) {
	if px < 1 || box.W < 1 || box.H < 1 {
		return
	}
	b := img.Bounds()
	x0, y0 := box.X, box.Y
	x1, y1 := box.X+box.W, box.Y+box.H
	for t := 0; t < px; t++ {
		for x := x0; x < x1; x++ {
			setNRGBA(img, b, x, y0+t, c)
			setNRGBA(img, b, x, y1-1-t, c)
		}
		for y := y0; y < y1; y++ {
			setNRGBA(img, b, x0+t, y, c)
			setNRGBA(img, b, x1-1-t, y, c)
		}
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

func strokeCircle(img *image.NRGBA, box Box) {
	s := box.W
	if box.H < s {
		s = box.H
	}
	cx := box.X + box.W/2
	cy := box.Y + box.H/2
	r := s / 2
	if r < 2 {
		return
	}
	b := img.Bounds()
	outer := r
	inner := r - gazeCalloutPx
	if inner < 1 {
		inner = 1
	}
	for y := cy - outer; y <= cy + outer; y++ {
		for x := cx - outer; x <= cx + outer; x++ {
			dx := x - cx
			dy := y - cy
			d2 := dx*dx + dy*dy
			if d2 <= outer*outer && d2 >= inner*inner {
				setNRGBA(img, b, x, y, gazeCalloutColor)
			}
		}
	}
}

func setNRGBA(img *image.NRGBA, b image.Rectangle, x, y int, c color.NRGBA) {
	if x < b.Min.X || x >= b.Max.X || y < b.Min.Y || y >= b.Max.Y {
		return
	}
	i := img.PixOffset(x, y)
	img.Pix[i+0] = c.R
	img.Pix[i+1] = c.G
	img.Pix[i+2] = c.B
	img.Pix[i+3] = c.A
}

func cloneNRGBA(src image.Image) *image.NRGBA {
	bounds := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(dst, dst.Bounds(), src, bounds.Min, draw.Src)
	return dst
}
