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

// ADR-9 的回归用例：父层素材里的子元素必须被抹成父层底色，
// 否则子元素会跟着父层的不透明度出现，自己声明的 startMs 形同失效。
func TestBuildLayerAssetsErasesNestedChild(t *testing.T) {
	white := color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}
	cardColor := color.NRGBA{0xF1, 0xF5, 0xF9, 0xFF}
	blue := color.NRGBA{0x1E, 0x3A, 0x8A, 0xFF}
	page := flatPage(200, 100, white)
	fillRect(page, 20, 20, 180, 90, cardColor)
	fillRect(page, 40, 40, 140, 60, blue)

	assets := BuildLayerAssets(page, []Layer{
		layer("card", Box{X: 20, Y: 20, W: 160, H: 70}),
		layer("bullet", Box{X: 40, Y: 40, W: 100, H: 20}),
	}, 200, 100)
	if len(assets) != 2 {
		t.Fatalf("want 2 assets, got %d", len(assets))
	}

	card := assets[0]
	if card.ElementID != "card" || card.Index != 0 {
		t.Fatalf("asset[0] = %s#%d, want card#0", card.ElementID, card.Index)
	}
	if got := card.Image.Bounds(); got.Dx() != 160 || got.Dy() != 70 {
		t.Fatalf("card asset bounds = %v, want 160x70", got)
	}
	if len(card.Erased) != 1 || card.Erased[0] != "bullet" {
		t.Errorf("card should erase its child, erased = %v", card.Erased)
	}
	// 卡片素材坐标 = 页坐标 − (20,20)；子元素中心 (90,50) → 素材 (70,30)。
	if got := card.Image.At(70, 30); !sameColor(got, cardColor) {
		t.Errorf("nested child not erased from card asset: %v", got)
	}
}

// 反向：子层素材必须保留包住它的父层背景，抹了就相当于把自己打穿。
func TestBuildLayerAssetsKeepsEnclosingBackground(t *testing.T) {
	white := color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}
	cardColor := color.NRGBA{0xF1, 0xF5, 0xF9, 0xFF}
	blue := color.NRGBA{0x1E, 0x3A, 0x8A, 0xFF}
	page := flatPage(200, 100, white)
	fillRect(page, 20, 20, 180, 90, cardColor)
	fillRect(page, 40, 40, 140, 60, blue)

	assets := BuildLayerAssets(page, []Layer{
		layer("card", Box{X: 20, Y: 20, W: 160, H: 70}),
		layer("bullet", Box{X: 40, Y: 40, W: 100, H: 20}),
	}, 200, 100)
	bullet := assets[1]
	if len(bullet.Erased) != 0 {
		t.Errorf("enclosing layer must not be erased, got %v", bullet.Erased)
	}
	// 自己还在：子层坐标 (50,10) → 页坐标 (90,50)。
	if got := bullet.Image.At(50, 10); !sameColor(got, blue) {
		t.Errorf("child pixel lost: %v", got)
	}
	// 裁切图只覆盖元素自己的盒子，盒子之外本来就不在素材里；
	// 「保留父层背景」的含义是：父层的填色没有被涂到子层上，子层四角仍是自己的像素。
	if got := bullet.Image.At(0, 0); !sameColor(got, blue) {
		t.Errorf("enclosing fill leaked into child asset: %v", got)
	}
}

// 无重叠的兄弟元素互不干扰：各自素材就是原图对应区域的拷贝。
func TestBuildLayerAssetsLeavesSiblingsIntact(t *testing.T) {
	white := color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}
	blue := color.NRGBA{0x1E, 0x3A, 0x8A, 0xFF}
	page := flatPage(200, 100, white)
	fillRect(page, 10, 10, 60, 40, blue)
	fillRect(page, 120, 10, 170, 40, blue)

	assets := BuildLayerAssets(page, []Layer{
		layer("a", Box{X: 10, Y: 10, W: 50, H: 30}),
		layer("b", Box{X: 120, Y: 10, W: 50, H: 30}),
	}, 200, 100)
	if len(assets) != 2 {
		t.Fatalf("want 2 assets, got %d", len(assets))
	}
	for _, asset := range assets {
		if len(asset.Erased) != 0 {
			t.Errorf("%s erased siblings: %v", asset.ElementID, asset.Erased)
		}
		if got := asset.Image.At(25, 15); !sameColor(got, blue) {
			t.Errorf("%s lost its own pixels: %v", asset.ElementID, got)
		}
	}
}

// 元素几乎占满整页时没有外环可取色：该层不出素材（调用方按缺省处理），
// 而不是拿一个猜出来的颜色去抹别人的画面。
func TestBuildLayerAssetsSkipsWhenNoRingAvailable(t *testing.T) {
	page := flatPage(60, 60, color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF})
	if assets := BuildLayerAssets(page, []Layer{
		layer("full", Box{X: 0, Y: 0, W: 60, H: 60}),
	}, 60, 60); len(assets) != 0 {
		t.Fatalf("full-page box should produce no asset, got %d", len(assets))
	}
}
