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
		// 镜头图与层图同为「同一页的额外出图」，混进页图会让按页计价的预算虚高。
		"slides/003-shot-01.png": KindLayers,
		"audio/mix.wav":          KindAudio,
		"audio/001.wav":          KindAudio,
		"app.js":                 KindPlayer,
		"app.css":                KindPlayer,
		"manifest.json":          KindManifest,
		"manifest.js":            KindManifest,
		"timeline.json":          KindManifest,
		"subtitles.vtt":          KindSubtitles,
		"subtitles.js":           KindSubtitles,
		"readme.txt":             KindOther,
	}
	for name, want := range cases {
		if got := classifyEntry(name); got != want {
			t.Fatalf("classifyEntry(%q) = %q, want %q", name, got, want)
		}
	}
}

// 单价来自真实素材实测（见 measure.go 注释）。这里是钉住换算关系：
// 14 页 / 2 分 20 秒的 customer-deck 实测 1.62 MiB，预算必须留出 1.5× 上下的余量——
// 太紧会让新品类的真实页面（大图页、满版截图）天天红，太宽则拦不住回归。
func TestBudgetMatchesRealDeckMeasurement(t *testing.T) {
	tier := CodecTier{Page: "webp", Audio: "aac"}
	got := tier.Budget(14, 0, 140_000_000, 14)
	const measured = 1_698_607 // tmp/webp-exp/full2-pkg 实测量
	if float64(got) < float64(measured)*1.3 || float64(got) > float64(measured)*1.7 {
		t.Fatalf("14 页 / 140s 预算 = %.2f MiB，实测 %.2f MiB，余量应在 1.3–1.7×",
			float64(got)/(1<<20), float64(measured)/(1<<20))
	}
	// 旧模型这里是 4.31 MiB：宽到包膨胀 2.7 倍也不红。
	if got > 3<<20 {
		t.Fatalf("预算没有收紧：%.2f MiB", float64(got)/(1<<20))
	}
}

func TestBudgetTiersDifferByCodec(t *testing.T) {
	lossy := CodecTier{Page: "webp", Audio: "aac"}.Budget(20, 0, 300_000_000, 20)
	jpeg := CodecTier{Page: "jpeg", Audio: "aac"}.Budget(20, 0, 300_000_000, 20)
	raw := CodecTier{Page: "png", Audio: "wav"}.Budget(20, 0, 300_000_000, 20)
	if !(lossy < jpeg && jpeg < raw) {
		t.Fatalf("档位没有拉开：webp=%.2f jpeg=%.2f raw=%.2f MiB",
			float64(lossy)/(1<<20), float64(jpeg)/(1<<20), float64(raw)/(1<<20))
	}
	// 有 ffmpeg 的机器：20 页 / 5 分钟预算 ≈ 4.30 MiB，与方案 §20.6 的 4.2 MB 目标同量级。
	if lo, hi := int64(4<<20), int64(5<<20); lossy < lo || lossy > hi {
		t.Fatalf("webp+aac 20 页 / 5 分钟预算 = %.2f MiB，want 4–5 MiB", float64(lossy)/(1<<20))
	}
	// 未转码档必须宽到能装下实测的 15.1 MiB（30 页 5 分钟 WAV/JPEG）。
	if raw30 := (CodecTier{Page: "jpeg", Audio: "wav"}).Budget(30, 0, 300_000_000, 30); raw30 < 16<<20 {
		t.Fatalf("jpeg+wav 30 页 / 5 分钟预算 = %.2f MiB，装不下实测 15.1 MiB", float64(raw30)/(1<<20))
	}
}

// 纯图文包不随时长增长：按分钟给预算等于白送，真正的回归会藏在这段余量里。
func TestBudgetIgnoresDurationWithoutAudio(t *testing.T) {
	tier := CodecTier{Page: "webp", Audio: "aac"}
	if got, want := tier.Budget(10, 0, 600_000_000, 0), tier.Budget(10, 0, 0, 0); got != want {
		t.Fatalf("无音频时预算仍随时长增长：%d vs %d", got, want)
	}
}

func TestBudgetCountsLayersAndPages(t *testing.T) {
	tier := CodecTier{Page: "webp", Audio: "none"}
	base := tier.Budget(5, 0, 0, 0)
	withLayers := tier.Budget(5, 3, 0, 0)
	withPages := tier.Budget(8, 0, 0, 0)
	if !(withLayers > base && withPages > base) {
		t.Fatalf("页数/层数没有进入预算：base=%d layers=%d pages=%d", base, withLayers, withPages)
	}
}

func TestDetectCodecTier(t *testing.T) {
	files := map[string][]byte{
		"slides/001.webp":         {1},
		"slides/002.webp":         {1},
		"slides/003.jpg":          {1}, // 单页退回 JPEG：按多数仍判 webp
		"slides/001-layer-00.png": {1},
		"audio/001.m4a":           {1},
		"audio/mix.m4a":           {1, 2, 3},
	}
	if got, want := detectCodecTier(files).String(), "webp+aac"; got != want {
		t.Fatalf("tier = %q, want %q", got, want)
	}

	// 音频按字节最多的扩展名判：混音轨是大头，分段备份不能把它带偏。
	wavMix := map[string][]byte{
		"slides/001.jpg": {1},
		"audio/mix.wav":  {1, 2, 3, 4, 5},
		"audio/001.m4a":  {1},
	}
	if got, want := detectCodecTier(wavMix).String(), "jpeg+wav"; got != want {
		t.Fatalf("tier = %q, want %q", got, want)
	}

	// 读不出编码时给最宽档，不要因为推断失败把正常包判红。
	if got, want := detectCodecTier(map[string][]byte{}).String(), "png+none"; got != want {
		t.Fatalf("tier = %q, want %q", got, want)
	}
}

func TestMeasureFilesReportsTier(t *testing.T) {
	files := map[string][]byte{
		FileAppJS:         make([]byte, 1024),
		"slides/001.webp": make([]byte, 2048),
		"audio/mix.m4a":   make([]byte, 4096),
	}
	m := MeasureFiles(files, nil)
	if m.CodecTier != "webp+aac" {
		t.Fatalf("codecTier = %q, want webp+aac", m.CodecTier)
	}
	if m.BudgetBytes <= 0 || m.OverBudget {
		t.Fatalf("budget = %d over = %v", m.BudgetBytes, m.OverBudget)
	}
	// 无清单时页数/层数为 0，预算只剩基数——不能把「没有清单」当成「没有内容」而除以零。
	if m.BudgetBytes != budgetBaseBytes {
		t.Fatalf("budget = %d, want base %d", m.BudgetBytes, budgetBaseBytes)
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
