package render

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"os"
	"path/filepath"
)

// 与 shared/motion.ts FOCUS_DIM_OPACITY=0.55 对齐：非焦点区盖白 1-0.55。
const gazePaintVeilAlpha = 0.45

var (
	gazeCalloutColor = color.NRGBA{R: 0xff, G: 0xd1, B: 0x66, A: 0xff}
	gazeCalloutPx    = 3
)

// gazePaintOK 是可以绕开 Chrome 的纯注视页：整页 PNG + 矩形纱/框，没有入场层、
// 切近、overlay。入场页仍走 Remotion 仍帧（底板挖洞 + 层图）。
func gazePaintOK(p Page) bool {
	if !isGazeLookPage(p) {
		return false
	}
	if HasEntranceElementLayer(p.Layers) {
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
		for li, w := range windows {
			img := paintGazeLook(base, page, w.MidMs, plan.Width, plan.Height)
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
	dst := cloneNRGBA(src)
	b := dst.Bounds()
	if canvasW <= 0 {
		canvasW = b.Dx()
	}
	if canvasH <= 0 {
		canvasH = b.Dy()
	}
	hole, veil := gazeHoleAt(page, tMs)
	overlayWhite(dst, veil)
	if hole != nil {
		copyBox(dst, src, *hole, canvasW, canvasH)
	}
	for _, r := range page.ClearRects {
		copyBox(dst, src, r, canvasW, canvasH)
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
