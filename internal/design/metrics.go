package design

import (
	"fmt"
	"sort"
)

// Severity 是发现项的严重级别。
type Severity string

const (
	// SeverityError 阻断：可读性问题，没有商量空间。
	SeverityError Severity = "error"
	// SeverityWarn 建议：来自参考片归纳的启发式，指出风险但不替代判断。
	SeverityWarn Severity = "warn"
)

// 观感度量规则编号。刻意用 P 前缀，与动画规格的 V1–V12 分开命名：
// 同一编号体系下出现两套含义，是后来最难查的那种混乱。
const (
	RuleFontSize     = "P1" // 正文最小字号
	RuleContrast     = "P2" // 文本对比度
	RuleElementCount = "P3" // 主要元素数 7±2
	RuleWhitespace   = "P4" // 留白比例
	RuleDensity      = "P5" // 节奏密度（clip 数/秒）
	RuleBeat         = "P6" // 每页节拍数
)

const (
	// MaxPrimaryElements 是单页主要元素上限（7±2 的上界，设计方案 §19.6）。
	MaxPrimaryElements = 9
	// MinWhitespaceRatio 是留白比例下限。低于它页面会显得"塞满"。
	MinWhitespaceRatio = 0.35
	// MaxClipsPerSecond 是节奏密度建议上限（§19.4/§19.6：clip 数/秒 ≤ 3）。
	MaxClipsPerSecond = 3.0
	// HardMaxClipsPerSecond 是阻断上限：到这一档不是"节奏快"，是"每个元素都在动"。
	HardMaxClipsPerSecond = 6.0
	// MinBeats / MaxBeats 是每页节拍数区间（§19.4：3–5）。
	MinBeats = 3
	MaxBeats = 5
	// MinBeatCheckDurationMS 以下不检查节拍数：短页本来就只有 1–2 拍。
	MinBeatCheckDurationMS = 4000
)

// PageFacts 是一页的观感事实。全部为纯数值，且 0 一律表示"未知"——
// 未知即跳过该项检查，不猜测。这与动画规格里 DurationMS=0 的语义一致。
type PageFacts struct {
	SlideID         string
	DurationMS      int64   // 页时长；0 = 未知（时间轴未生成）
	ElementCount    int     // 主要元素数（文本块 + 图像 + 图表，不含装饰）
	MinBodyFontPx   float64 // 页内最小正文字号；0 = 未知
	MinTextContrast float64 // 页内最低文本对比度；0 = 未知
	OccupiedRatio   float64 // 元素占用面积 / 画布面积；0 = 未知
	ClipCount       int     // 动画片段总数
	BeatCount       int     // 节拍数；0 = 未知
}

// Finding 是一条度量结果，带实测值与阈值，便于回溯"当时为什么判过"。
type Finding struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Field    string   `json:"field"`
	Value    float64  `json:"value"`
	Limit    float64  `json:"limit"`
	Message  string   `json:"message"`
	Hint     string   `json:"hint"`
}

// Report 是一页的度量报告。
type Report struct {
	SlideID  string    `json:"slideId"`
	Findings []Finding `json:"findings"`
}

// OK 判断是否可放行：只要没有 error 即为通过，warn 不阻断。
func (r Report) OK() bool {
	for _, f := range r.Findings {
		if f.Severity == SeverityError {
			return false
		}
	}
	return true
}

// BySeverity 返回指定级别的发现项（顺序与报告一致）。
func (r Report) BySeverity(s Severity) []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Severity == s {
			out = append(out, f)
		}
	}
	return out
}

// CheckPage 对一页做观感度量。纯函数：同一输入必得同一报告（无时钟、无 I/O、无随机）。
func CheckPage(f PageFacts) Report {
	r := Report{SlideID: f.SlideID}
	add := func(fd Finding) { r.Findings = append(r.Findings, fd) }

	// P1 正文最小字号（error）
	if f.MinBodyFontPx > 0 && f.MinBodyFontPx < MinBodyFontPx {
		add(Finding{
			Code: RuleFontSize, Severity: SeverityError, Field: "minBodyFontPx",
			Value: f.MinBodyFontPx, Limit: MinBodyFontPx,
			Message: fmt.Sprintf("正文最小字号 %.0fpx 低于 %.0fpx", f.MinBodyFontPx, MinBodyFontPx),
			Hint:    "改小字号不如改内容：把这段移出正文（caption 仅限页脚/来源），或拆页",
		})
	}

	// P2 文本对比度（error）
	if f.MinTextContrast > 0 && f.MinTextContrast < MinContrastRatio {
		add(Finding{
			Code: RuleContrast, Severity: SeverityError, Field: "minTextContrast",
			Value: f.MinTextContrast, Limit: MinContrastRatio,
			Message: fmt.Sprintf("最低文本对比度 %.2f:1 低于 %.1f:1", f.MinTextContrast, MinContrastRatio),
			Hint:    "换用色板中的 ink/inkBody/inkMuted，或改用较深的 paperAlt 背景",
		})
	}

	// P3 主要元素数（warn）：只查上界。少元素往往是有意的（封面、引文、单数字）。
	if f.ElementCount > MaxPrimaryElements {
		add(Finding{
			Code: RuleElementCount, Severity: SeverityWarn, Field: "elementCount",
			Value: float64(f.ElementCount), Limit: MaxPrimaryElements,
			Message: fmt.Sprintf("主要元素 %d 个，超过建议上限 %d 个（7±2）", f.ElementCount, MaxPrimaryElements),
			Hint:    "拆成两页，或把次要信息降级为 caption/口播",
		})
	}

	// P4 留白比例（warn）
	if f.OccupiedRatio > 0 {
		whitespace := 1 - f.OccupiedRatio
		if whitespace < MinWhitespaceRatio {
			add(Finding{
				Code: RuleWhitespace, Severity: SeverityWarn, Field: "occupiedRatio",
				Value: f.OccupiedRatio, Limit: 1 - MinWhitespaceRatio,
				Message: fmt.Sprintf("元素占用 %.0f%%，留白仅 %.0f%%，低于 %.0f%%",
					f.OccupiedRatio*100, whitespace*100, MinWhitespaceRatio*100),
				Hint: "缩小视觉元素或删掉一个信息块；满版会让观众找不到焦点",
			})
		}
	}

	// P5 节奏密度（warn / error 两级）
	if f.DurationMS > 0 && f.ClipCount > 0 {
		rate := float64(f.ClipCount) / (float64(f.DurationMS) / 1000)
		switch {
		case rate > HardMaxClipsPerSecond:
			add(Finding{
				Code: RuleDensity, Severity: SeverityError, Field: "clipsPerSecond",
				Value: round2(rate), Limit: HardMaxClipsPerSecond,
				Message: fmt.Sprintf("每秒 %.1f 个动效，超过硬上限 %.0f（每个元素都在动）", rate, HardMaxClipsPerSecond),
				Hint:    "合并同一动作组的 clip，或改为页面级转场；先把入场动效减到 3 条以内",
			})
		case rate > MaxClipsPerSecond:
			add(Finding{
				Code: RuleDensity, Severity: SeverityWarn, Field: "clipsPerSecond",
				Value: round2(rate), Limit: MaxClipsPerSecond,
				Message: fmt.Sprintf("每秒 %.1f 个动效，超过建议上限 %.0f", rate, MaxClipsPerSecond),
				Hint:    "用 stagger 让同组元素共享一条时间轴，而不是各自起一条 clip",
			})
		}
	}

	// P6 每页节拍数（warn）
	if f.DurationMS >= MinBeatCheckDurationMS && f.BeatCount > 0 {
		if f.BeatCount < MinBeats || f.BeatCount > MaxBeats {
			add(Finding{
				Code: RuleBeat, Severity: SeverityWarn, Field: "beatCount",
				Value: float64(f.BeatCount), Limit: MaxBeats,
				Message: fmt.Sprintf("每页 %d 拍，超出建议区间 %d–%d", f.BeatCount, MinBeats, MaxBeats),
				Hint:    "按单拍 900–1400ms 重排：拍太少会拖，拍太多会赶",
			})
		}
	}

	return r
}

// CheckAll 批量度量，保持入参顺序。
func CheckAll(facts []PageFacts) []Report {
	out := make([]Report, 0, len(facts))
	for _, f := range facts {
		out = append(out, CheckPage(f))
	}
	return out
}

// Summary 汇总一批报告的规则命中次数（按规则编号排序，便于打印）。
func Summary(reports []Report) map[string]int {
	out := map[string]int{}
	for _, r := range reports {
		for _, f := range r.Findings {
			out[f.Code]++
		}
	}
	return out
}

// SortedFindingCodes 返回命中过的规则编号（稳定排序）。
func SortedFindingCodes(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}
