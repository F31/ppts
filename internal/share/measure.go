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
	KindSubtitles = "subtitles"
	KindManifest  = "manifest"
	KindOther     = "other"
)

// 预算基数与每分钟增量（design ADR-6）。
//
// 基数 3 MiB 覆盖播放器 + 20 页图 + 清单；每分钟 0.55 MiB 覆盖 AAC 64kbps 单声道
// （≈0.48 MiB/min）再留 15% 余量。5 分钟 → 5.75 MiB，比方案 §20.6 的 4.2 MB 目标宽 37%，
// 因为字体子集与 WebP 都还没做：阈值取「当前实现的合理上界」，否则门禁第一天就永久红。
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
