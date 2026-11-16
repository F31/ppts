package render

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"
)

func solidNRGBA(w, h int, body color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, body)
		}
	}
	if h > 0 {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, h-1, color.NRGBA{R: 255, A: 255})
		}
	}
	return img
}

func TestCropSlackDropsExtraRow(t *testing.T) {
	src := solidNRGBA(1920, 1081, color.NRGBA{G: 80, A: 255})
	got := CropSlack(src, 1920, 1080)
	if got.Bounds().Dx() != 1920 || got.Bounds().Dy() != 1080 {
		t.Fatalf("got %v, want 1920×1080", got.Bounds())
	}
	r, _, _, _ := got.At(0, 1079).RGBA()
	if r>>8 == 255 {
		t.Fatal("bottom marker row leaked into 1080 crop")
	}
}

func TestCropSlackLeavesExactAndOversized(t *testing.T) {
	exact := solidNRGBA(1920, 1080, color.NRGBA{B: 40, A: 255})
	if CropSlack(exact, 1920, 1080) != exact {
		t.Fatal("exact canvas must return src")
	}
	big := solidNRGBA(2000, 1125, color.NRGBA{B: 40, A: 255})
	if CropSlack(big, 1920, 1080) != big {
		t.Fatal("80px overflow must leave src (that is a different raster, not the 1px pdftoppm miss)")
	}
	small := solidNRGBA(1919, 1080, color.NRGBA{B: 40, A: 255})
	if CropSlack(small, 1920, 1080) != small {
		t.Fatal("smaller source must leave src")
	}
}

func TestSnapRasterSlackOffice16x9(t *testing.T) {
	src := solidNRGBA(1920, 1081, color.NRGBA{G: 80, A: 255})
	got := SnapRasterSlack(src)
	if got.Bounds().Dx() != 1920 || got.Bounds().Dy() != 1080 {
		t.Fatalf("snap 1920×1081 = %v, want 1920×1080", got.Bounds())
	}
	fourByThree := solidNRGBA(1440, 1080, color.NRGBA{G: 80, A: 255})
	if SnapRasterSlack(fourByThree) != fourByThree {
		t.Fatal("4:3 must not snap toward 16:9")
	}
}

func TestCropStagedPNGRewritesSlackFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "page.png")
	if err := writePNGFile(path, solidNRGBA(1920, 1081, color.NRGBA{G: 80, A: 255})); err != nil {
		t.Fatal(err)
	}
	if err := cropStagedPNG(path, 1920, 1080); err != nil {
		t.Fatalf("cropStagedPNG: %v", err)
	}
	got, err := readImage(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Bounds().Dx() != 1920 || got.Bounds().Dy() != 1080 {
		t.Fatalf("staged file = %v, want 1920×1080", got.Bounds())
	}
}

func TestCropStagedPNGIgnoresNonPNG(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "page.png")
	if err := os.WriteFile(path, []byte("page-zero"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cropStagedPNG(path, 1920, 1080); err != nil {
		t.Fatalf("non-png must not fail: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "page-zero" {
		t.Fatalf("placeholder bytes changed: %v %q", err, got)
	}
}
