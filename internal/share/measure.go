package share

import (
	"sort"
	"strings"
)

// 体积类别。分类的意义在于「超预算时知道该优化哪一项」——
// 只报一个总数，人会先去压图片，而实际大头可能是 WAV 音频（本仓库当前就是）。
const (
	KindPages     = "pages"
	KindLayers    = "layers"
	KindAudio     = "audio"
	KindPlayer    = "player"
	KindFont      = "font"
	KindSubtitles = "subtitles"
	KindManifest  = "manifest"
	KindOther     = "other"
)

// 预算基数与每分钟增量（design ADR-6）。
//
// 基数 3 MiB 覆盖播放器 + 20 页图 + 清单；每分钟 0.5625 MiB 覆盖 AAC 64kbps 单声道
// （≈0.48 MiB/min）再留 15% 余量。5 分钟 → 5.81 MiB，比方案 §20.6 的 4.2 MB 目标宽 38%。
//
// 三项体积手段现已全部落地：字体按片字符裁到约 87 KB（ADR-028）；混音轨 5 分钟
// 14.4 MB WAV → 2.4 MB m4a（ADR-032）；页图 JPEG q82 → WebP q75，真实素材实测
// 体积 41.4% 且质量指标更优（ADR-034）。5 分钟包 15.1 → 3.7 MiB，页图类回到 1 MiB 量级。
//
// 为什么**仍然**不收紧阈值：收紧要用真实素材的分布做基线，而当前夹具里的页图是
// 程序合成的（大面积纯色 + 色条），WebP 在它上面的收益与真实页面不具可比性。
// 先让真实导出的包跑一段，拿到「页图类」的实际分布再整体重订；在那之前收紧，
// 门禁只会在真实素材上永久红，然后被人加 `--skip` 绕过——那比阈值宽更糟。
//
// 注意音频与页图这两条的「已落地」都是有条件的：机器上没有 ffmpeg（或该 ffmpeg
// 不带 libwebp）时打包器退回 WAV / JPEG，包照样能播，只是体积回到数倍。所以报告
// 必须给出实际编码（share-verify 的 audioCodec / pageCodec），否则同一份代码在不同
// 机器上产出两个量级的包，而报告只显示「体积」。
const (
	budgetBaseBytes   int64 = 3 << 20
	budgetBytesPerMin int64 = 589824 // 0.5625 MiB，取整便于核对
)

// Measurement 是一份成品包的体积构成。
type Measurement struct {
	TotalBytes int64            `json:"totalBytes"`
	ByKind     map[string]int64 `json:"byKind"`
	// Pages / Layers 是计数而非字节：层图丢了与「这页本来没动画」在体积上无法区分。
	Pages          int   `json:"pages"`
	Layers         int   `json:"layers"`
	AnimatedSlides int   `json:"animatedSlides"`
	AudioSegments  int   `json:"audioSegments"`
	DurationUS     int64 `json:"durationUs"`

	BudgetBytes int64 `json:"budgetBytes"`
	OverBudget  bool  `json:"overBudget"`
}

// MeasureFiles 按类别统计包内字节。
func MeasureFiles(files map[string][]byte, manifest *Manifest) Measurement {
	m := Measurement{ByKind: map[string]int64{}}
	for name, data := range files {
		size := int64(len(data))
		m.TotalBytes += size
		m.ByKind[classifyEntry(name)] += size
	}
	if manifest != nil {
		m.Pages = len(manifest.Slides)
		for _, slide := range manifest.Slides {
			m.Layers += len(slide.Layers)
			if len(slide.Layers) > 0 {
				m.AnimatedSlides++
			}
		}
		m.AudioSegments = len(manifest.Audio)
		m.DurationUS = manifest.Artifact.DurationUS
	}
	m.BudgetBytes = BudgetBytes(m.DurationUS)
	m.OverBudget = m.BudgetBytes > 0 && m.TotalBytes > m.BudgetBytes
	return m
}

// BudgetBytes 按片长给出预算；片长未知（0）时只用基数，避免把未知当成"很短"。
func BudgetBytes(durationUS int64) int64 {
	if durationUS <= 0 {
		return budgetBaseBytes
	}
	minutes := float64(durationUS) / 60e6
	return budgetBaseBytes + int64(minutes*float64(budgetBytesPerMin))
}

// SortedKinds 按字节数降序返回类别，便于输出「从大到小」的构成表。
func (m Measurement) SortedKinds() []string {
	kinds := make([]string, 0, len(m.ByKind))
	for kind := range m.ByKind {
		kinds = append(kinds, kind)
	}
	sort.Slice(kinds, func(i, j int) bool {
		if m.ByKind[kinds[i]] != m.ByKind[kinds[j]] {
			return m.ByKind[kinds[i]] > m.ByKind[kinds[j]]
		}
		return kinds[i] < kinds[j]
	})
	return kinds
}

func classifyEntry(name string) string {
	switch {
	case name == FileAppJS || name == FileAppCSS:
		return KindPlayer
	case name == FileFontCSS || strings.HasPrefix(name, "assets/fonts/"):
		// 字体单列一类：它和「播放器」的优化手段完全不同（一个是改编码、一个是改字集大小），
		// 混在一起会让超预算时看不出该动哪一项。
		return KindFont
	case strings.HasPrefix(name, "audio/"):
		return KindAudio
	case name == FileManifestJSON || name == FileManifestJS || name == FileTimelineJSON:
		return KindManifest
	case name == FileSubtitlesVTT || name == FileSubtitlesSRT || name == FileSubtitlesJS:
		return KindSubtitles
	case strings.HasPrefix(name, "slides/"):
		// 层图命名是 slides/NNN-layer-MM.png（layerEntryPattern），与页图同目录。
		if strings.Contains(name, "-layer-") {
			return KindLayers
		}
		return KindPages
	default:
		return KindOther
	}
}
