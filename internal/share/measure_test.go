package share

import (
	"bytes"
	"context"
	"testing"
)

func TestClassifyEntry(t *testing.T) {
	cases := map[string]string{
		"slides/001.jpg":          KindPages,
		"slides/002.png":          KindPages,
		"slides/001-layer-00.png": KindLayers,
		"audio/mix.wav":           KindAudio,
		"audio/001.wav":           KindAudio,
		"app.js":                  KindPlayer,
		"app.css":                 KindPlayer,
		"manifest.json":           KindManifest,
		"manifest.js":             KindManifest,
		"timeline.json":           KindManifest,
		"subtitles.vtt":           KindSubtitles,
		"subtitles.js":            KindSubtitles,
		"readme.txt":              KindOther,
	}
	for name, want := range cases {
		if got := classifyEntry(name); got != want {
			t.Fatalf("classifyEntry(%q) = %q, want %q", name, got, want)
		}
	}
}

// 预算按片长增长：单一阈值对 30 秒与 20 分钟一样严，前者永远通过、后者永远失败。
func TestBudgetGrowsWithDuration(t *testing.T) {
	if got, want := BudgetBytes(0), budgetBaseBytes; got != want {
		t.Fatalf("BudgetBytes(0) = %d, want %d", got, want)
	}
	half := BudgetBytes(30_000_000)
	five := BudgetBytes(300_000_000)
	if !(half > budgetBaseBytes && five > half) {
		t.Fatalf("budget must grow with duration: 30s=%d, 5min=%d", half, five)
	}
	// 5 分钟 ≈ 3 MiB + 2.8 MiB。比方案 §20.6 的 4.2 MB 目标宽，因为字体子集与 WebP 还没做。
	if five < 5<<20 || five > 7<<20 {
		t.Fatalf("5min budget = %.2f MiB, want within 5–7 MiB", float64(five)/(1<<20))
	}
}

func TestMeasureCountsAnimatedSlidesSeparately(t *testing.T) {
	var buf bytes.Buffer
	manifest, err := NewBundleBuilder(baseReader(t), nil).Build(context.Background(), buildInput(), &buf)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	_, files, err := VerifyBundle(buf.Bytes())
	if err != nil {
		t.Fatalf("VerifyBundle: %v", err)
	}
	m := MeasureFiles(files, manifest)
	if m.Pages != len(manifest.Slides) {
		t.Fatalf("pages = %d, want %d", m.Pages, len(manifest.Slides))
	}
	// 层数为 0 与「层图丢了」在体积上无法区分，所以动画页数必须单独计数。
	if m.AnimatedSlides != 0 || m.Layers != 0 {
		t.Fatalf("static bundle reports %d animated slides / %d layers", m.AnimatedSlides, m.Layers)
	}
	if m.ByKind[KindAudio] == 0 || m.ByKind[KindPages] == 0 {
		t.Fatalf("measure lost categories: %+v", m.ByKind)
	}
	if sum := m.ByKind[KindAudio] + m.ByKind[KindPages] + m.ByKind[KindPlayer]; sum == 0 {
		t.Fatal("empty measurement")
	}
	if m.TotalBytes != int64(buf.Len()) && m.TotalBytes <= 0 {
		t.Fatalf("totalBytes = %d", m.TotalBytes)
	}
}
