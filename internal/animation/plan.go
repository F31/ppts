package animation

import (
	"fmt"
	"math"
)

// BaselineHeight 是 catalog 中 params 数值的单位基准：1080p 画面高度下的像素。
// 编译器把它按目标画布高度等比换算，因此同一份规格在 720p / 1080p / 4K 上观感一致。
const BaselineHeight = 1080

// 属性名白名单。IR 只允许这些属性，渲染后端只负责插值，不解释动效语义。
const (
	PropOpacity    = "opacity"
	PropTranslateX = "translateX"
	PropTranslateY = "translateY"
	PropScale      = "scale"
	PropScaleX     = "scaleX"
	PropScaleY     = "scaleY"
)

// MotionProp 是一条属性轨道：从 From 线性（按缓动）过渡到 To。
// 单位约定：opacity/scale 为无量纲；translate 为 BaselineHeight 基准像素。
type MotionProp struct {
	Name string  `json:"name"`
	From float64 `json:"from"`
	To   float64 `json:"to"`
}

// CubicBezier 是 CSS cubic-bezier(x1,y1,x2,y2) 的四个控制点。
// 用数值而不是曲线名传给渲染器：曲线名一旦在两端各自映射，就会漂移。
type CubicBezier struct {
	X1 float64 `json:"x1"`
	Y1 float64 `json:"y1"`
	X2 float64 `json:"x2"`
	Y2 float64 `json:"y2"`
}

// EasingCurves 是规格里四个缓动名的唯一定义。
var EasingCurves = map[string]CubicBezier{
	"linear": {X1: 0, Y1: 0, X2: 1, Y2: 1},
	// CSS ease-out：起步快、收尾稳，入场动效的默认选择。
	"easeOut": {X1: 0.22, Y1: 1, X2: 0.36, Y2: 1},
	// 两头缓、中间快，用于生长/位移幅度较大的场合。
	"easeInOut": {X1: 0.65, Y1: 0, X2: 0.35, Y2: 1},
	// 回弹：控制点 1.42 实测过冲约 7.3%，落在视觉基线 Overshoot.MaxAmplitude(8%) 之内。
	// 再大就会突破上限——曲线幅度与档案声明的上限必须是同一个数。
	"spring": {X1: 0.34, Y1: 1.42, X2: 0.64, Y2: 1},
}

// DefaultEasing 是缓动名缺省时的取值。
const DefaultEasing = "easeOut"

// EasingCurve 解析缓动名。空串按 DefaultEasing 处理。
func EasingCurve(name string) (CubicBezier, error) {
	if name == "" {
		name = DefaultEasing
	}
	c, ok := EasingCurves[name]
	if !ok {
		return CubicBezier{}, fmt.Errorf("animation: unknown easing %q", name)
	}
	return c, nil
}

// MotionPlan 是一个动效展开后的数值定义。
type MotionPlan struct {
	Easing string
	Curve  CubicBezier
	Props  []MotionProp
}

// 动效参数缺省值（catalog 未声明该参数时使用）。
const (
	defaultSlideOffset = 40.0
	defaultScaleFrom   = 0.88
	// bob 在 catalog 里只有 loop 参数，没有幅度：给一个 1080p 下 10px 的固定幅度。
	// 再大就不像"轻微浮动"，而像"元素在跳"。
	bobAmplitude = 10.0
)

// PlanMotion 把动效展开为属性轨道。
//
// 这是"动效语义"的唯一实现点：两个渲染后端都不再认识 fadeIn/slideIn 这些名字，
// 只插值这里给出的数值。新增动效只需在此增加分支，后端零改动。
//
// intensity 只缩放幅度，不改变时长——时长是用户在编辑器里看得见、且已被 V5 校验的值。
func PlanMotion(effectID string, p Params, easing string, intensity float64) (MotionPlan, error) {
	eff, ok := Catalog().Effect(effectID)
	if !ok {
		return MotionPlan{}, fmt.Errorf("animation: unknown effect %q", effectID)
	}
	curve, err := EasingCurve(easing)
	if err != nil {
		return MotionPlan{}, err
	}
	k := clampIntensity(intensity)

	props, err := expandEffect(eff, p, k)
	if err != nil {
		return MotionPlan{}, err
	}
	return MotionPlan{Easing: nameOfEasing(easing), Curve: curve, Props: props}, nil
}

func nameOfEasing(name string) string {
	if name == "" {
		return DefaultEasing
	}
	return name
}

func clampIntensity(v float64) float64 {
	if math.IsNaN(v) || v <= 0 {
		return 1
	}
	if v < MinIntensity {
		return MinIntensity
	}
	if v > MaxIntensity {
		return MaxIntensity
	}
	return v
}

func expandEffect(eff *Effect, p Params, k float64) ([]MotionProp, error) {
	switch eff.ID {
	case "fadeIn":
		return []MotionProp{{Name: PropOpacity, From: 0, To: 1}}, nil

	case "slideIn":
		// 规格里的 offset 优先；为 0 时取 catalog 默认值（Params 的零值无法区分"没填"和"填了 0"，
		// 而 0 位移等于纯淡入，本就该由 fadeIn 表达）。
		offset := p.Offset
		if offset == 0 {
			offset = paramNumber(eff, "offset", defaultSlideOffset)
		}
		offset *= k
		dir := p.Direction
		if dir == "" {
			dir = paramString(eff, "direction", "up")
		}
		// "up" 表示元素向上运动进入：起点在下方，即 translateY 为 +offset。
		props := []MotionProp{{Name: PropOpacity, From: 0, To: 1}}
		switch dir {
		case "up":
			props = append(props, MotionProp{Name: PropTranslateY, From: offset, To: 0})
		case "down":
			props = append(props, MotionProp{Name: PropTranslateY, From: -offset, To: 0})
		case "left":
			props = append(props, MotionProp{Name: PropTranslateX, From: offset, To: 0})
		case "right":
			props = append(props, MotionProp{Name: PropTranslateX, From: -offset, To: 0})
		default:
			return nil, fmt.Errorf("animation: %s: unknown direction %q", eff.ID, dir)
		}
		return props, nil

	case "pop":
		scaleFrom := p.ScaleFrom
		if scaleFrom == 0 {
			scaleFrom = paramNumber(eff, "scaleFrom", defaultScaleFrom)
		}
		// 强度作用于"幅度"（1-scaleFrom），而不是 scaleFrom 本身：
		// 否则 intensity=0 会把元素缩到 88% 而不是不缩放。
		from := 1 - (1-scaleFrom)*k
		return []MotionProp{
			{Name: PropOpacity, From: 0, To: 1},
			{Name: PropScale, From: round3(from), To: 1},
		}, nil

	case "grow":
		axis := p.Axis
		if axis == "" {
			axis = paramString(eff, "axis", "y")
		}
		// 生长从接近 0 到 1；幅度由数据决定，强度不适用（不存在"长多少"这个可调量）。
		name := PropScaleY
		if axis == "x" {
			name = PropScaleX
		}
		return []MotionProp{
			{Name: PropOpacity, From: 0, To: 1},
			{Name: name, From: 0.04, To: 1},
		}, nil

	case "highlight":
		// 区域动画只能改透明度：不能描边、不能改色（底图已烘焙）。
		from := 1 - 0.55*k
		if from < 0.05 {
			from = 0.05
		}
		return []MotionProp{{Name: PropOpacity, From: round3(from), To: 1}}, nil

	case "bob":
		amp := bobAmplitude * k
		return []MotionProp{{Name: PropTranslateY, From: 0, To: -amp}}, nil

	case "fadeOut":
		return []MotionProp{{Name: PropOpacity, From: 1, To: 0}}, nil
	}
	return nil, fmt.Errorf("animation: effect %q has no property expansion (catalog 与 plan.go 不同步)", eff.ID)
}

// ValidProp 报告属性名是否在白名单内（IR 校验使用）。
func ValidProp(name string) bool {
	switch name {
	case PropOpacity, PropTranslateX, PropTranslateY, PropScale, PropScaleX, PropScaleY:
		return true
	}
	return false
}

func paramNumber(eff *Effect, key string, fallback float64) float64 {
	spec, ok := eff.Params[key]
	if !ok || spec.Type != "number" || spec.Default.Kind != "number" {
		return fallback
	}
	return spec.Default.Number
}

func paramString(eff *Effect, key, fallback string) string {
	spec, ok := eff.Params[key]
	if !ok || spec.Type != "enum" || spec.Default.Kind != "string" {
		return fallback
	}
	return spec.Default.String
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
