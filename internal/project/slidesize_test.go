package project

import "testing"

func TestRasterDPIOfficeScreen16x9Hits1080p(t *testing.T) {
	got := RasterDPI(9144000, 5143500)
	if got != 192 {
		t.Fatalf("10in 16:9 DPI = %d, want 192 (1920×1080 native)", got)
	}
}

func TestRasterDPIClassicOnScreenKeeps150(t *testing.T) {
	got := RasterDPI(12192000, 6858000)
	if got != 150 {
		t.Fatalf("13.3in 16:9 DPI = %d, want 150 (already ≥1920)", got)
	}
}

func TestRasterDPIMissingSizeFallsBack(t *testing.T) {
	if got := RasterDPI(0, 0); got != 150 {
		t.Fatalf("missing size DPI = %d, want 150", got)
	}
}

func TestRasterDPIPortraitUsesShortEdge(t *testing.T) {
	got := RasterDPI(5143500, 9144000)
	if got != 192 {
		t.Fatalf("10in 9:16 DPI = %d, want 192", got)
	}
}

func TestRasterSizeOfficeScreen16x9Is1080p(t *testing.T) {
	w, h := RasterSize(9144000, 5143500, 192)
	if w != 1920 || h != 1080 {
		t.Fatalf("10in 16:9 @192 = %dx%d, want 1920×1080", w, h)
	}
}

func TestRasterSizeClassicOnScreenKeepsNative(t *testing.T) {
	w, h := RasterSize(12192000, 6858000, 150)
	if w != 2000 || h != 1125 {
		t.Fatalf("13.3in 16:9 @150 = %dx%d, want 2000×1125", w, h)
	}
}

func TestRasterSizeMissingInputIsZero(t *testing.T) {
	if w, h := RasterSize(0, 0, 192); w != 0 || h != 0 {
		t.Fatalf("missing EMU = %dx%d, want 0×0", w, h)
	}
	if w, h := RasterSize(9144000, 5143500, 0); w != 0 || h != 0 {
		t.Fatalf("missing DPI = %dx%d, want 0×0", w, h)
	}
}
