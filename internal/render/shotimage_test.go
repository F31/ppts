package render

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// 这两组用例守的是同一件事的两端：
//   - CropShot 把画布坐标映射到源图像素时不能差一像素（差一像素不报错，只表现为「元素飘了」）；
//   - staging 真的把镜头图落到了 IR 声明的路径上——在此之前渲染侧没有任何发布者，
//     Timeline.tsx 用 staticFile 读 slides/NNN-shot-NN.png，读到的是一个不存在的文件。

func gradientImage(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: uint8((x + y) % 251), A: 255})
		}
	}
	return img
}

func TestCropShotMapsCanvasToSourcePixels(t *testing.T) {
	// 源图是画布的两倍：150 DPI 页图（长边 1600）对 800 宽画布就是这个比例。
	// 倍率算错时裁出来的区域仍然「是一张完整的图」，只有像素比对看得见。
	src := gradientImage(200, 100)
	dst, err := CropShot(src, Box{X: 10, Y: 10, W: 20, H: 10}, 100, 50)
	if err != nil {
		t.Fatalf("CropShot: %v", err)
	}
	if got := dst.Bounds(); got.Dx() != 40 || got.Dy() != 20 {
		t.Fatalf("crop size = %dx%d, want 40x20", got.Dx(), got.Dy())
	}
	// 逐像素对源图：画布 (10,10) 对应源 (20,20)。
	for y := 0; y < 20; y++ {
		for x := 0; x < 40; x++ {
			want := src.(*image.RGBA).RGBAAt(20+x, 20+y)
			if got := dst.RGBAAt(x, y); got != want {
				t.Fatalf("pixel (%d,%d) = %v, want %v (source 20+%d,20+%d)", x, y, got, want, x, y)
			}
		}
	}
}

func TestCropShotClampsToSource(t *testing.T) {
	// 越界的裁切框不会报错，只会读到图像外的像素（黑色）。钳制到源图范围，
	// 宁可裁小一点，也不要交出一张右边带黑边的特写。
	src := gradientImage(100, 100)
	dst, err := CropShot(src, Box{X: 90, Y: 90, W: 20, H: 20}, 100, 100)
	if err != nil {
		t.Fatalf("CropShot: %v", err)
	}
	if got := dst.Bounds(); got.Dx() != 10 || got.Dy() != 10 {
		t.Fatalf("clamped crop = %dx%d, want 10x10", got.Dx(), got.Dy())
	}
	if got := dst.RGBAAt(0, 0); got != (color.RGBA{R: 90, G: 90, B: 180, A: 255}) {
		t.Fatalf("clamped pixel = %v, want source (90,90)", got)
	}
}

func TestCropShotRejectsDegenerateInput(t *testing.T) {
	src := gradientImage(10, 10)
	if _, err := CropShot(src, Box{X: 0, Y: 0, W: 0, H: 5}, 10, 10); err == nil {
		t.Fatal("zero-width crop must be rejected")
	}
	if _, err := CropShot(src, Box{X: 0, Y: 0, W: 5, H: 5}, 0, 10); err == nil {
		t.Fatal("zero canvas width must be rejected")
	}
}

// shotFixture 造一页带真页图与一组镜头的计划，并把页图写进 dir。
func shotFixture(t *testing.T, withLayer bool) (*Timeline, string) {
	t.Helper()
	plan, dir := stageFixture(t, 1)
	plan.Pages[0].DurationMs = 3000
	if !withLayer {
		plan.Pages[0].Layers = nil
	}
	plan.Pages[0].Shots = []Shot{
		{Index: 0, Image: plan.Pages[0].BaseImage, StartMs: 0, DurationMs: 2000},
		{Index: 1, Image: "slides/001-shot-01.png",
			Crop:    &Box{X: 8, Y: 4, W: 32, H: 18},
			StartMs: 2000, DurationMs: 1000, CrossFadeMs: 100},
	}
	return plan, dir
}

func readPNG(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return img
}

func TestStagePagesPublishesShotImages(t *testing.T) {
	plan, dir := shotFixture(t, true)
	staged, err := (&RemotionBackend{}).stagePages(plan, dir, []int{0})
	if err != nil {
		t.Fatalf("stagePages: %v", err)
	}

	path := filepath.Join(dir, "slides", "001-shot-01.png")
	img := readPNG(t, path)
	// 64×36 画布、64×36 页图 → 倍率 1，裁切框即源像素框。
	if got := img.Bounds(); got.Dx() != 32 || got.Dy() != 18 {
		t.Fatalf("shot image = %dx%d, want 32x18", got.Dx(), got.Dy())
	}
	// 左上角对应源图 (8,4)：stageFixture 画的是 R=x*3、G=y*5、B=200。
	if got, want := img.At(0, 0), (color.RGBA{R: 24, G: 20, B: 200, A: 255}); got != want {
		t.Fatalf("shot pixel (0,0) = %v, want %v", got, want)
	}

	// 首镜必须改指向底板：编译期还没有底板，它填的是 baseImage；不改的话，
	// 合成器会先画「元素还在的整页」再把元素从零淡入一遍——重影。
	if staged.Pages[0].PlateImage == "" {
		t.Fatal("page with element layer must have a plate")
	}
	if got := staged.Pages[0].Shots[0].Image; got != staged.Pages[0].PlateImage {
		t.Fatalf("first shot image = %q, want plate %q", got, staged.Pages[0].PlateImage)
	}
	// staging 不改调用方计划：Shots 是切片，只复制 Page 结构体会把写入落回原切片。
	if got := plan.Pages[0].Shots[0].Image; got != "pages/p0.png" {
		t.Fatalf("caller plan mutated: first shot image = %q", got)
	}
}

func TestStagePagesPublishesShotsWithoutLayers(t *testing.T) {
	// 「只切镜、没有分层动画」是最常见用法。旧的 staging 在没有元素层时直接 return，
	// 这条路径上镜头图永远不生成——成片会退回整页，看起来像切镜没生效。
	plan, dir := shotFixture(t, false)
	staged, err := (&RemotionBackend{}).stagePages(plan, dir, []int{0})
	if err != nil {
		t.Fatalf("stagePages: %v", err)
	}
	if staged.Pages[0].PlateImage != "" {
		t.Fatal("page without element layers must not get a plate")
	}
	if got := staged.Pages[0].Shots[0].Image; got != "pages/p0.png" {
		t.Fatalf("first shot must stay on the base image, got %q", got)
	}
	readPNG(t, filepath.Join(dir, "slides", "001-shot-01.png"))
}

func TestStagePagesRejectsShotWithoutCrop(t *testing.T) {
	plan, dir := shotFixture(t, false)
	plan.Pages[0].Shots[1].Crop = nil
	if _, err := (&RemotionBackend{}).stagePages(plan, dir, []int{0}); err == nil {
		t.Fatal("a non-first shot without crop must fail staging, not silently render the whole page")
	}
}
