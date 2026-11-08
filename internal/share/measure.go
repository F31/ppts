package share

import (
	"path"
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

// 体积预算模型：base + 每页 + 每层 + 每分钟音频（design ADR-6，2026-11 重订）。
//
// 旧模型是「3 MiB 基数 + 0.5625 MiB/分钟」，两个数都是估的：基数按「20 页图」估，
// 增量按「AAC 64kbps 再留 15%」估。三项体积手段（字体子集 ADR-028 / AAC ADR-032 /
// WebP ADR-034）陆续落地后，这份预算比实测宽 2.7 倍——14 页 / 2 分 20 秒的真实包
// 实测 1.62 MiB，旧预算 4.31 MiB。阈值宽不是「安全」，是「失效」：包膨胀到 3.5 MiB
// 也不会红，等发现时已经在用户手机上下不动了。
//
// 现在的单价全部来自真实素材实测（`sample-customer-deck.pptx` 14 页，经 WSL
// LibreOffice → PDF → pdftoppm 200 DPI 渲染，与产品渲染链同源；时长 140 秒、14 段旁白）：
//
//	固定开销　播放器 27.7 KB + 字体 87 KB + 清单/字幕/杂项 20 KB ≈ 134 KB
//	　　　　　→ 取 0.35 MiB（2.7× 余量，留给播放器长功能、多字体家族）
//	页图　　　WebP q75 均值 35.3 KB、最大 42.6 KB（长边 1600）
//	　　　　　→ 取 0.06 MiB/页（1.7× 余量）
//	音频　　　AAC 64k 单声道实测 0.467 MiB/min → 取 0.55 MiB/min（1.18× 余量）
//
// 按此模型：14 页 / 2 分 20 秒 → 2.47 MiB（实测占 65%）；20 页 / 5 分钟 → 4.30 MiB，
// 与方案 §20.6 的 4.2 MB 目标同量级（旧模型 5.81 MiB，宽 44%）。
//
// 为什么必须分档：同一条链路在「有 ffmpeg + libwebp」与「没有」时产出的包差一个数量级
// （同一份 30 页 5 分钟素材：15.1 MiB 的 WAV/JPEG 包 vs 3.7 MiB 的 AAC/WebP 包）。
// 一套阈值套两种产出，降级环境会永久红，然后被人用 `--skip` 绕过——那比阈值宽更糟。
// 所以预算按包内**实际编码**取档（档位从条目扩展名推断，share-verify 会打印），
// 降级档的单价按未转码实测值给：JPEG q82 86.2 KB/页、WAV 2.75 MiB/min。
//
// 层图单价是这张表里唯一没有真实样本的：share 侧只原样写入上层裁好的 PNG，而手头
// 没有带元素动画的真实导出包（夹具里的层图是合成的纯色块，0.8 KB/层，毫无参考价值）。
// 暂按页图单价的 2 倍占位（透明 PNG 比同尺寸 WebP 大）。**首次真实动画导出后必须复核**：
// 那时若红，先量层图再改这里，不要直接放宽阈值。
const budgetBaseBytes int64 = (1 << 20) * 35 / 100 // 0.35 MiB

// pctMiB 把 MiB 的百分比写成整数，便于与注释里的数字逐个对齐。
func pctMiB(percent int64) int64 { return (1 << 20) * percent / 100 }

// CodecTier 是这份包的编码档位。空字段按最坏档处理：读不出编码时宁可给宽预算，
// 也不要因为推断失败把一个正常包判红。
type CodecTier struct {
	Page  string `json:"page"`  // webp | jpeg | png
	Audio string `json:"audio"` // aac | mp3 | opus | wav | none
}

func (t CodecTier) String() string { return t.Page + "+" + t.Audio }

// pageRates 返回每页页图与每层层图的预算。
func (t CodecTier) pageRates() (page, layer int64) {
	switch t.Page {
	case "webp":
		return pctMiB(6), pctMiB(12)
	case "jpeg":
		return pctMiB(13), pctMiB(26)
	default: // png 或推断不出：按未转码实测值
		return pctMiB(35), pctMiB(70)
	}
}

// minuteRate 返回每分钟音频预算。只有 wav 走未转码档：它的体积是常数，
// 不需要留余量；有损档（aac/mp3/opus）统一按 AAC 实测值 + 18%。
func (t CodecTier) minuteRate() int64 {
	switch t.Audio {
	case "aac", "mp3", "opus":
		return pctMiB(55)
	default:
		return pctMiB(300)
	}
}

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

	// CodecTier 是预算取的档位；报告里必须给出来，否则同一份代码在不同机器上
	// 产出两个量级的包，而报告只显示「体积」，看不出是阈值变了还是包坏了。
	CodecTier   string `json:"codecTier"`
	BudgetBytes int64  `json:"budgetBytes"`
	OverBudget  bool   `json:"overBudget"`
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
	tier := detectCodecTier(files)
	m.CodecTier = tier.String()
	m.BudgetBytes = tier.Budget(m.Pages, m.Layers, m.DurationUS, m.AudioSegments)
	m.OverBudget = m.TotalBytes > m.BudgetBytes
	return m
}

// Budget 按页数、层数、时长与编码档位给出预算。
//
// 时长只在**有音频**时计价：纯图文包（无旁白）的体积不随时长增长，按分钟给它预算
// 等于白送，会让真正的回归藏进余量里。
func (t CodecTier) Budget(pages, layers int, durationUS int64, audioSegments int) int64 {
	page, layer := t.pageRates()
	budget := budgetBaseBytes + int64(pages)*page + int64(layers)*layer
	if audioSegments > 0 && durationUS > 0 {
		minutes := float64(durationUS) / 60e6
		budget += int64(minutes * float64(t.minuteRate()))
	}
	return budget
}

// detectCodecTier 从条目扩展名推断编码档位。
//
// 页图取「多数」而不是「任一」：WebP 逐页退回 JPEG 是允许的（大图 WebP 反而更大时），
// 少量退回不该把整包降到 JPEG 档。音频取字节最多的那个：混音轨是大头，分段音频是备份。
func detectCodecTier(files map[string][]byte) CodecTier {
	pageCount := map[string]int{}
	audioBytes := map[string]int64{}
	for name, data := range files {
		switch {
		case isPageEntry(name):
			pageCount[pageCodecOf(name)]++
		case strings.HasPrefix(name, "audio/"):
			audioBytes[audioCodecOf(name)] += int64(len(data))
		}
	}
	tier := CodecTier{Page: "png", Audio: "none"}
	if codec, n := majority(pageCount); n > 0 {
		tier.Page = codec
	}
	if codec, n := majorityBytes(audioBytes); n > 0 {
		tier.Audio = codec
	}
	return tier
}

func isPageEntry(name string) bool {
	if !strings.HasPrefix(name, "slides/") {
		return false
	}
	// 层图与镜头图是同一页的额外出图，不计入「页数」那一档。
	return !strings.Contains(name, "-layer-") && !strings.Contains(name, "-shot-")
}

func pageCodecOf(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".webp":
		return "webp"
	case ".jpg", ".jpeg":
		return "jpeg"
	default:
		return "png"
	}
}

func audioCodecOf(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".m4a", ".aac", ".mp4":
		return "aac"
	case ".mp3":
		return "mp3"
	case ".opus", ".ogg":
		return "opus"
	case ".wav":
		return "wav"
	default:
		return "other"
	}
}

func majority(counts map[string]int) (string, int) {
	best, bestN := "", 0
	for codec, n := range counts {
		if n > bestN || (n == bestN && codec < best) {
			best, bestN = codec, n
		}
	}
	return best, bestN
}

func majorityBytes(sizes map[string]int64) (string, int64) {
	best, bestN := "", int64(0)
	for codec, n := range sizes {
		if n > bestN || (n == bestN && codec < best) {
			best, bestN = codec, n
		}
	}
	return best, bestN
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
		// 层图 slides/NNN-layer-MM.png（layerEntryPattern）与镜头图 slides/NNN-shot-MM.png
		// 都是「同一页的额外出图」。漏掉 shot 会让多镜头包的页图字节虚高，
		// 而预算的每页单价是按页数给的——两边口径必须一致。
		if strings.Contains(name, "-layer-") || strings.Contains(name, "-shot-") {
			return KindLayers
		}
		return KindPages
	default:
		return KindOther
	}
}
