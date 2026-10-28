package render

import (
	"image"
	"image/color"
	"testing"
)

func flatPage(w, h int, bg color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, bg)
		}
	}
	return img
}

func fillRect(img *image.NRGBA, x0, y0, x1, y1 int, c color.NRGBA) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}

func layer(elementID string, box Box) Layer {
	return Layer{ElementID: elementID, Kind: "title", AssetMode: AssetBitmapRegion, Box: box}
}

// 纯色底上的元素必须被完整抹平，且补丁颜色等于底色——这是「去残影」成立的充要条件。
func TestBuildPlateFillsElementWithSurroundingColor(t *testing.T) {
	white := color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}
	blue := color.NRGBA{0x1E, 0x3A, 0x8A, 0xFF}
	page := flatPage(200, 100, white)
	fillRect(page, 20, 20, 120, 80, blue)

	box := Box{X: 20, Y: 20, W: 100, H: 60}
	plate, patches := BuildPlate(page, []Layer{layer("title", box)}, 200, 100)

	if len(patches) != 1 {
		t.Fatalf("want 1 patch, got %d", len(patches))
	}
	if patches[0].Noisy {
		t.Errorf("flat background should not be noisy (spread=%d)", patches[0].Spread)
	}
	if patches[0].Color != white {
		t.Errorf("patch color = %v, want white", patches[0].Color)
	}
	for y := box.Y; y < box.Y+box.H; y++ {
		for x := box.X; x < box.X+box.W; x++ {
			if got := plate.At(x, y); got != white {
				t.Fatalf("plate at (%d,%d) = %v, want white", x, y, got)
			}
		}
	}
	// 盒子之外必须原样保留：补洞只处理元素区域，不该把整页洗白。
	if got := plate.At(5, 5); got != white {
		t.Errorf("outside pixel altered: %v", got)
	}
	if got := plate.At(150, 50); got != white {
		t.Errorf("pixel right of box altered: %v", got)
	}
}

// 调用方传进来的图片不能被改动：底板是新图，原图还要用来裁图层素材。
func TestBuildPlateDoesNotMutateSource(t *testing.T) {
	white := color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}
	blue := color.NRGBA{0x1E, 0x3A, 0x8A, 0xFF}
	page := flatPage(80, 80, white)
	fillRect(page, 10, 10, 50, 50, blue)

	BuildPlate(page, []Layer{layer("title", Box{X: 10, Y: 10, W: 40, H: 40})}, 80, 80)

	if got := page.NRGBAAt(20, 20); got != blue {
		t.Errorf("source mutated: at (20,20) = %v, want blue", got)
	}
}

// 照片/渐变底：补丁照做，但必须留 noise 标记，否则「画面有道色块」会变成无人认领的 bug。
func TestBuildPlateMarksNoisyBackground(t *testing.T) {
	page := image.NewNRGBA(image.Rect(0, 0, 120, 120))
	for y := 0; y < 120; y++ {
		for x := 0; x < 120; x++ {
			// 每 4px 一个色带，制造明显极差。
			shade := uint8(40 + (x/4)*8)
			page.SetNRGBA(x, y, color.NRGBA{shade, shade, shade, 0xFF})
		}
	}
	_, patches := BuildPlate(page, []Layer{layer("title", Box{X: 40, Y: 40, W: 40, H: 40})}, 120, 120)
	if len(patches) != 1 {
		t.Fatalf("want 1 patch, got %d", len(patches))
	}
	if !patches[0].Noisy {
		t.Errorf("gradient background should be noisy (spread=%d)", patches[0].Spread)
	}
}

// 整页图层（elementId 为空）不参与补洞：它本来就是底图。
func TestBuildPlateIgnoresPageLayer(t *testing.T) {
	page := flatPage(60, 60, color.NRGBA{0xEE, 0xEE, 0xEE, 0xFF})
	_, patches := BuildPlate(page, []Layer{layer("", Box{X: 0, Y: 0, W: 60, H: 60})}, 60, 60)
	if len(patches) != 0 {
		t.Fatalf("page layer should not be patched, got %d patches", len(patches))
	}
}

// 元素几乎占满整页时没有外环可采样：跳过补洞，而不是拿全图均值糊上去。
func TestBuildPlateSkipsWhenNoRingAvailable(t *testing.T) {
	page := flatPage(60, 60, color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF})
	_, patches := BuildPlate(page, []Layer{layer("title", Box{X: 0, Y: 0, W: 60, H: 60})}, 60, 60)
	if len(patches) != 0 {
		t.Fatalf("full-page box should produce no patch, got %d", len(patches))
	}
}

// 底图分辨率高于画布时（导出放大过），盒子要按比例换算，否则补丁会错位。
func TestBuildPlateScalesBoxToImageSize(t *testing.T) {
	white := color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}
	blue := color.NRGBA{0x1E, 0x3A, 0x8A, 0xFF}
	// 画布 100x50，底图 200x100：两倍缩放。
	page := flatPage(200, 100, white)
	fillRect(page, 40, 40, 140, 100, blue)

	plate, patches := BuildPlate(page, []Layer{layer("title", Box{X: 20, Y: 20, W: 50, H: 30})}, 100, 50)
	if len(patches) != 1 {
		t.Fatalf("want 1 patch, got %d", len(patches))
	}
	// 画布盒 (20,20,50,30) → 图像盒 (40,40,100,60)。
	if got := plate.At(60, 60); got != white {
		t.Errorf("scaled box not filled: %v", got)
	}
}
