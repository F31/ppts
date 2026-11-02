package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/F31/ppts/internal/render"
)

// writeTestPNG 造一张纯色 PNG。只关心尺寸，所以内容任意。
func writeTestPNG(t *testing.T, path string, w, h int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		t.Fatalf("encode: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// shotPlan 造一份「一页两镜」的计划：画布 640×360，特写裁 (40,160 560×160)。
func shotPlan() *render.Timeline {
	return &render.Timeline{
		Width:  640,
		Height: 360,
		Pages: []render.Page{{
			BaseImage: "selftest/p000.png",
			Shots: []render.Shot{
				{Index: 0, Image: "selftest/p000.png", DurationMs: 750},
				{
					Index:      1,
					Image:      "slides/000-shot-01.png",
					Crop:       &render.Box{X: 40, Y: 160, W: 560, H: 160},
					StartMs:    750,
					DurationMs: 750,
				},
			},
		}},
	}
}

func TestCheckSelftestShotsAcceptsMatchingSize(t *testing.T) {
	dir := t.TempDir()
	writeTestPNG(t, filepath.Join(dir, "selftest", "p000.png"), 640, 360)
	writeTestPNG(t, filepath.Join(dir, "slides", "000-shot-01.png"), 560, 160)

	if err := checkSelftestShots(shotPlan(), dir); err != nil {
		t.Fatalf("尺寸对得上却报错: %v", err)
	}
}

// 页图是画布的 2 倍时，特写也必须是 2 倍：这条断言的意义就在于它不是
// 「文件存在就行」，而是按实际倍率算期望值。生产页图是 300 DPI。
func TestCheckSelftestShotsScalesWithSource(t *testing.T) {
	plan := shotPlan()
	dir := t.TempDir()
	writeTestPNG(t, filepath.Join(dir, "selftest", "p000.png"), 1280, 720)
	writeTestPNG(t, filepath.Join(dir, "slides", "000-shot-01.png"), 1120, 320)

	if err := checkSelftestShots(plan, dir); err != nil {
		t.Fatalf("2× 页图下应期望 1120×320: %v", err)
	}

	// 同一份计划、同样 2× 页图，但镜头图是按 1× 裁的——正是「staging 用错倍率」
	// 会留下的痕迹，必须红。
	writeTestPNG(t, filepath.Join(dir, "slides", "000-shot-01.png"), 560, 160)
	if err := checkSelftestShots(plan, dir); err == nil {
		t.Fatal("镜头图按 1× 裁时应判失败")
	}
}

func TestCheckSelftestShotsRejectsMissingAsset(t *testing.T) {
	dir := t.TempDir()
	writeTestPNG(t, filepath.Join(dir, "selftest", "p000.png"), 640, 360)

	err := checkSelftestShots(shotPlan(), dir)
	if err == nil {
		t.Fatal("镜头图缺失时应判失败")
	}
}

// 夹具里没有特写镜时，这条检查什么都没验——不能算通过。
func TestCheckSelftestShotsRejectsFixtureWithoutCloseUp(t *testing.T) {
	plan := shotPlan()
	plan.Pages[0].Shots = plan.Pages[0].Shots[:1]

	dir := t.TempDir()
	writeTestPNG(t, filepath.Join(dir, "selftest", "p000.png"), 640, 360)

	err := checkSelftestShots(plan, dir)
	if err == nil {
		t.Fatal("没有特写镜时应报「未被覆盖」，不能静默通过")
	}
}
