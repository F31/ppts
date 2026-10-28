package design

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Range 是闭区间。用于一切"设计上允许的取值区间"，避免散落的 min/max 常量。
type Range struct {
	Min, Max float64
}

// Contains 判断 v 是否落在区间内（含端点）。
func (r Range) Contains(v float64) bool { return v >= r.Min && v <= r.Max }

// Mid 返回区间中点，用于"没有更好依据时取中间值"。
func (r Range) Mid() float64 { return (r.Min + r.Max) / 2 }

func (r Range) String() string {
	return fmt.Sprintf("%.0f–%.0f", r.Min, r.Max)
}

// 速度分层（设计方案 §19.4）：大元素 600–900ms、小元素 240–400ms，中档取其间。
const (
	TierLarge  = "large"
	TierMedium = "medium"
	TierSmall  = "small"
)

// 面积占比阈值：视觉重量由元素占画布面积决定，而非元素类型。
// 大块色板要"重"、行内小图标要"轻"，用同一个时长会让画面失去层次。
const (
	LargeElementAreaRatio  = 0.25
	MediumElementAreaRatio = 0.08
)

// Anticipation 是预备动作：在正向前先反向位移一小段，制造"发力"感。
type Anticipation struct {
	Ratio       float64 // 占 clip 时长的比例（§19.4：15%）
	MinOffsetPx float64 // 反向位移下限
	MaxOffsetPx float64 // 反向位移上限
}

// Overshoot 是回弹：越过终点再回来。
type Overshoot struct {
	Damping      Range   // spring damping 区间（§19.4：12–16）
	MaxAmplitude float64 // 回弹幅度上限，相对位移的比例（§19.4：≤8%）
}

// Beat 是节拍：一页由几个"动作单元"组成，每单元多长。
type Beat struct {
	PerPage Range // 每页节拍数（§19.4：3–5）
	UnitMs  Range // 单拍时长（§19.4：900–1400ms）
}

// Easing 是命名三次贝塞尔缓动。控制点即 CSS cubic-bezier 的四个参数。
type Easing struct {
	Name string
	P1x  float64
	P1y  float64
	P2x  float64
	P2y  float64
}

// CSS 返回 cubic-bezier 表达式。
func (e Easing) CSS() string {
	return fmt.Sprintf("cubic-bezier(%.2f,%.2f,%.2f,%.2f)", e.P1x, e.P1y, e.P2x, e.P2y)
}

func bezier1(p1, p2, s float64) float64 {
	// 端点固定为 0 与 1。
	u := 1 - s
	return 3*u*u*s*p1 + 3*u*s*s*p2 + s*s*s
}

// At 求值：输入时间进度 t∈[0,1]，输出位移进度（可能因回弹而 >1）。
// 先在 x 上反解参数 s（二分，固定 32 次迭代：确定性、无随机、无浮点累积误差），
// 再在 y 上求值。这与 CSS 的 timing function 语义一致。
func (e Easing) At(t float64) float64 {
	if t <= 0 {
		return 0
	}
	if t >= 1 {
		return 1
	}
	lo, hi := 0.0, 1.0
	for i := 0; i < 32; i++ {
		mid := (lo + hi) / 2
		if bezier1(e.P1x, e.P2x, mid) < t {
			lo = mid
		} else {
			hi = mid
		}
	}
	return bezier1(e.P1y, e.P2y, (lo+hi)/2)
}

// Peak 返回采样区间内的最大输出值，用于断言回弹幅度。
func (e Easing) Peak(samples int) float64 {
	if samples < 2 {
		samples = 2
	}
	max := 0.0
	for i := 0; i <= samples; i++ {
		if v := e.At(float64(i) / float64(samples)); v > max {
			max = v
		}
	}
	return max
}

// StaggerMode 是组内元素的出现顺序。
type StaggerMode string

const (
	// StaggerSequential 从左到右/从上到下依次出现。
	StaggerSequential StaggerMode = "sequential"
	// StaggerCenter 由中心向两侧扩散，用于强调"一个整体同时展开"。
	StaggerCenter StaggerMode = "center"
)

// MotionProfile 是运动质感参数集合。它是"高级感"的可评审部分：
// 数值而非实现，渲染器按它排时序。
type MotionProfile struct {
	Stagger      Range        // 组内错峰间隔
	Anticipation Anticipation // 预备动作
	Overshoot    Overshoot    // 回弹
	HoldMs       int64        // 关键动作后的静置下限
	SpeedTiers   map[string]Range
	Beat         Beat
	Easings      []Easing
}

// DefaultMotion 返回运动质感基线，数值对齐设计方案 §19.4。
func DefaultMotion() MotionProfile {
	return MotionProfile{
		Stagger:      Range{Min: 60, Max: 90},
		Anticipation: Anticipation{Ratio: 0.15, MinOffsetPx: 4, MaxOffsetPx: 8},
		Overshoot:    Overshoot{Damping: Range{Min: 12, Max: 16}, MaxAmplitude: 0.08},
		HoldMs:       400,
		SpeedTiers: map[string]Range{
			TierLarge:  {Min: 600, Max: 900},
			TierMedium: {Min: 400, Max: 600},
			TierSmall:  {Min: 240, Max: 400},
		},
		Beat: Beat{PerPage: Range{Min: 3, Max: 5}, UnitMs: Range{Min: 900, Max: 1400}},
		Easings: []Easing{
			{Name: "standard", P1x: 0.20, P1y: 0, P2x: 0, P2y: 1},
			{Name: "enter", P1x: 0.05, P1y: 0.70, P2x: 0.10, P2y: 1},
			{Name: "exit", P1x: 0.35, P1y: 0, P2x: 0.80, P2y: 0.15},
			// 回弹曲线：控制点 1.42 实测过冲约 7.3%，落在 Overshoot.MaxAmplitude(8%) 之内。
			// 控制点再大就会突破上限——曲线的过冲幅度必须与档案声明的上限一致，否则两处各说一套。
			{Name: "overshoot", P1x: 0.34, P1y: 1.42, P2x: 0.64, P2y: 1},
		},
	}
}

// Easing 按名取缓动曲线。
func (m MotionProfile) Easing(name string) (Easing, bool) {
	for _, e := range m.Easings {
		if e.Name == name {
			return e, true
		}
	}
	return Easing{}, false
}

// TierFor 按元素面积占比返回速度分层名。
func (m MotionProfile) TierFor(areaRatio float64) string {
	switch {
	case areaRatio >= LargeElementAreaRatio:
		return TierLarge
	case areaRatio >= MediumElementAreaRatio:
		return TierMedium
	default:
		return TierSmall
	}
}

// DurationFor 返回某元素在给定兜底时长下的推荐时长（取区间中点，向下取整到 10ms）。
// 兜底时长用于页太短时压缩：不会超过它。
func (m MotionProfile) DurationFor(areaRatio float64, capMs int64) int64 {
	r, ok := m.SpeedTiers[m.TierFor(areaRatio)]
	if !ok {
		return 0
	}
	d := int64(math.Round(r.Mid()/10) * 10)
	if capMs > 0 && d > capMs {
		d = capMs
	}
	return d
}

// StaggerOffsets 生成组内 n 个元素的相对起始偏移（毫秒）。
// 顺序模式：0, step, 2*step…；中心模式：中心为 0，向外每层 +step（偶数个时中间两项并列）。
// 纯函数：同一输入必得同一序列。
func StaggerOffsets(n int, stepMs int64, mode StaggerMode) []int64 {
	if n <= 0 {
		return nil
	}
	out := make([]int64, n)
	if stepMs < 0 {
		stepMs = 0
	}
	switch mode {
	case StaggerCenter:
		for i := 0; i < n; i++ {
			// 到中心的距离（以半个元素为单位的整数倍），避免出现 0.5 层。
			d := 2*i - (n - 1)
			if d < 0 {
				d = -d
			}
			out[i] = int64((d+1)/2) * stepMs
		}
	default: // StaggerSequential
		for i := 0; i < n; i++ {
			out[i] = int64(i) * stepMs
		}
	}
	return out
}

// Validate 校验运动档案自身一致。
func (m MotionProfile) Validate() error {
	if err := validRange("stagger", m.Stagger, 1); err != nil {
		return err
	}
	if m.Anticipation.Ratio <= 0 || m.Anticipation.Ratio > 0.5 {
		return fmt.Errorf("design: 预备动作时长占比 %.2f 必须在 (0, 0.5] 内", m.Anticipation.Ratio)
	}
	if m.Anticipation.MinOffsetPx <= 0 || m.Anticipation.MaxOffsetPx < m.Anticipation.MinOffsetPx {
		return fmt.Errorf("design: 预备位移区间非法 %.1f–%.1f px", m.Anticipation.MinOffsetPx, m.Anticipation.MaxOffsetPx)
	}
	if err := validRange("回弹阻尼", m.Overshoot.Damping, 1); err != nil {
		return err
	}
	if m.Overshoot.MaxAmplitude <= 0 || m.Overshoot.MaxAmplitude > 0.2 {
		return fmt.Errorf("design: 回弹幅度上限 %.2f 必须在 (0, 0.2] 内", m.Overshoot.MaxAmplitude)
	}
	if m.HoldMs < 400 {
		return fmt.Errorf("design: 停顿下限 %dms 低于 §19.4 的 400ms", m.HoldMs)
	}
	if len(m.SpeedTiers) == 0 {
		return fmt.Errorf("design: 缺少速度分层")
	}
	names := make([]string, 0, len(m.SpeedTiers))
	for k := range m.SpeedTiers {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if err := validRange("速度分层 "+k, m.SpeedTiers[k], 1); err != nil {
			return err
		}
	}
	for _, k := range []string{TierLarge, TierMedium, TierSmall} {
		if _, ok := m.SpeedTiers[k]; !ok {
			return fmt.Errorf("design: 缺少速度分层 %s", k)
		}
	}
	// 大元素慢于小元素：反了就说明参数抄错了。
	if m.SpeedTiers[TierLarge].Min < m.SpeedTiers[TierSmall].Max {
		return fmt.Errorf("design: 大元素时长 %s 应不短于小元素 %s 的上限",
			m.SpeedTiers[TierLarge], m.SpeedTiers[TierSmall])
	}
	if err := validRange("每页节拍数", m.Beat.PerPage, 1); err != nil {
		return err
	}
	if err := validRange("单拍时长", m.Beat.UnitMs, 1); err != nil {
		return err
	}
	if len(m.Easings) == 0 {
		return fmt.Errorf("design: 缺少缓动曲线")
	}
	seen := map[string]bool{}
	for _, e := range m.Easings {
		if strings.TrimSpace(e.Name) == "" {
			return fmt.Errorf("design: 缓动曲线缺少名称")
		}
		if seen[e.Name] {
			return fmt.Errorf("design: 缓动曲线重名: %s", e.Name)
		}
		seen[e.Name] = true
		if e.P1x < 0 || e.P1x > 1 || e.P2x < 0 || e.P2x > 1 {
			return fmt.Errorf("design: 缓动 %s 的 x 控制点必须在 [0,1]（CSS 要求）", e.Name)
		}
	}
	for _, name := range []string{"standard", "enter", "exit", "overshoot"} {
		if !seen[name] {
			return fmt.Errorf("design: 缺少命名缓动 %s", name)
		}
	}
	return nil
}

func validRange(name string, r Range, min float64) error {
	if r.Min < min || r.Max < r.Min {
		return fmt.Errorf("design: %s 区间非法 %.1f–%.1f（下限需 ≥ %.1f）", name, r.Min, r.Max, min)
	}
	return nil
}
