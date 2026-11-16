package render

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func solidPNG(t *testing.T, w, h int, c color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	// 底行涂成可辨认的标记，裁掉后不应再出现。
	if h > 0 {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, h-1, color.NRGBA{R: 255, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

func TestCropPNGToDropsExtraRow(t *testing.T) {
	src := solidPNG(t, 1920, 1081, color.NRGBA{G: 80, A: 255})
	got, w, h, err := cropPNGTo(src, 1920, 1080)
	if err != nil {
		t.Fatalf("cropPNGTo: %v", err)
	}
	if w != 1920 || h != 1080 {
		t.Fatalf("size = %dx%d, want 1920×1080", w, h)
	}
	img, err := png.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("decode cropped: %v", err)
	}
	if img.Bounds().Dx() != 1920 || img.Bounds().Dy() != 1080 {
		t.Fatalf("decoded = %v, want 1920×1080", img.Bounds())
	}
	r, g, b, a := img.At(0, 1079).RGBA()
	if r>>8 == 255 && g>>8 == 0 && b>>8 == 0 && a>>8 == 255 {
		t.Fatal("bottom row of 1081 source leaked into cropped 1080")
	}
}

func TestCropPNGToLeavesExactAndOversized(t *testing.T) {
	exact := solidPNG(t, 1920, 1080, color.NRGBA{B: 40, A: 255})
	got, w, h, err := cropPNGTo(exact, 1920, 1080)
	if err != nil || w != 1920 || h != 1080 {
		t.Fatalf("exact: %dx%d err=%v", w, h, err)
	}
	if !bytes.Equal(got, exact) {
		t.Fatal("exact size must return the original bytes")
	}

	big := solidPNG(t, 2000, 1125, color.NRGBA{B: 40, A: 255})
	got, w, h, err = cropPNGTo(big, 1920, 1080)
	if err != nil {
		t.Fatalf("oversized: %v", err)
	}
	if w != 2000 || h != 1125 {
		t.Fatalf("oversized cropped to %dx%d, want leave 2000×1125", w, h)
	}
	if !bytes.Equal(got, big) {
		t.Fatal("overflow >2px must leave the original bytes")
	}
}

func TestCropPNGToSkipsWhenTargetZero(t *testing.T) {
	src := solidPNG(t, 320, 180, color.NRGBA{A: 255})
	got, w, h, err := cropPNGTo(src, 0, 0)
	if err != nil || w != 320 || h != 180 || !bytes.Equal(got, src) {
		t.Fatalf("zero target: %dx%d err=%v", w, h, err)
	}
}
