// Package render 定义目标无关的渲染输入（CompiledTimeline）与它的编译器、校验、
// 规范化哈希，以及渲染服务的 HTTP 契约与客户端。
//
// 分层约束（设计方案 §4.2）：本包属于 L3（编排）与 L4（渲染）的边界，允许依赖
// internal/animation（规格）与 internal/design（画布令牌），不得依赖 internal/api、
// internal/app、internal/media。渲染实现（光栅化）不在本包内，通过 Backend 接口注入。
//
// 核心不变式：IR 里没有动效名。渲染后端只做「属性 from → to + cubic-bezier」的插值，
// 因此 MP4 与 HTML 两个后端逐帧一致是构造出来的，而不是靠两边各自实现对。
package render

import (
	"fmt"
	"math"

	"github.com/F31/ppts/internal/animation"
)

// IRSchemaVersion 是 IR 的契约版本。改动字段语义必须递增，服务端据此拒绝旧版计划。
const IRSchemaVersion = "compiled-timeline/v1"

// 渲染参数的硬边界（IR 校验与服务端请求限制共用）。
const (
	MinFPS = 12
	MaxFPS = 60

	MaxPages = 500
	// MaxPlanBytes 是 POST /v1/render 的请求体上限。
	MaxPlanBytes = 16 << 20

	// MaxFocusFadeMS 是焦点切换的过渡上限。超过一秒的「渐隐渐显」会被看成
	// 画面在发灰，而不是注意力转移。
	MaxFocusFadeMS = 1000
)

// 资产模式（设计方案 §6.2）。首版只支持前两种：底图来自整页渲染的 PNG。
const (
	AssetBitmapRegion = "bitmap-region"
	AssetPageLayer    = "page-layer"
)

// Timeline 是一份渲染就绪的完整计划。
type Timeline struct {
	SchemaVersion string `json:"schemaVersion"`
	FPS           int    `json:"fps"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	// Background 是页面之间的底色（转场交叉时可见），CSS 颜色。
	Background string `json:"background"`
	// Fonts 参与 planHash：字体换版本，同一份计划必须重渲。
	Fonts []Font `json:"fonts,omitempty"`
	Pages []Page `json:"pages"`
	// Warnings 是编译诊断。它不影响像素，因此不参与 planHash。
	Warnings []Diagnostic `json:"warnings,omitempty"`
}

// Font 是渲染所用字体的族名与版本。
type Font struct {
	Family  string `json:"family"`
	Version string `json:"version"`
}

// Page 是一页的渲染指令。
type Page struct {
	Index      int    `json:"index"`
	SlideID    string `json:"slideId"`
	DurationMs int64  `json:"durationMs"`
	// BaseImage 是相对工作根目录的路径。绝对路径与 .. 一律被拒绝（§8.7）。
	BaseImage string `json:"baseImage"`
	// PlateImage 是「去掉元素本体」的底板（相对工作根目录）。
	//
	// 底图是整页烘焙的 PNG：把元素区域抬升成图层后，原位置仍留着本体，
	// 入场动画会看到静态副本与移动副本重叠（design.md ADR-6）。
	// 底板把那些区域用周围底色填平，图层再盖上去。
	//
	// 它是 baseImage + layers[*].box 的确定性派生物，因此**不参与 planHash**；
	// 缺省表示「没有元素级图层」或调用方未做补洞，合成器退回 baseImage。
	PlateImage string     `json:"plateImage,omitempty"`
	Layers     []Layer    `json:"layers,omitempty"`
	Transition Transition `json:"transition"`
	// Focus 是「这一刻讲的是谁」：按 StartMs 升序的焦点序列。
	//
	// 它不改变任何一层的动效，只告诉合成器**此刻谁该被让出来**——
	// 非焦点元素降透明度，焦点元素保持。没有它，一页内容在 1.4s 铺满之后
	// 就再没有变化，讲解还在继续（V8 的「静默」正是量这个）。
	//
	// 为什么放在页级而不是给每层加一条「压暗」clip：压暗是**互斥关系**
	// （A 亮则 B 暗），而 clip 的叠加规则是逐属性相加/相乘，无法表达互斥。
	// 页级序列只描述「此刻焦点是谁」，由求值器推导其余元素的压暗量，
	// 因此天然不会出现「两个元素同时被压暗一半」这类矛盾状态。
	Focus []FocusSpan `json:"focus,omitempty"`
}

// FocusSpan 是一段焦点区间：从 StartMs 起，焦点落在 ElementID 上，直到下一段开始。
type FocusSpan struct {
	StartMs int64 `json:"startMs"`
	// ElementID 必须是本页某个图层的 elementId。
	ElementID string `json:"elementId"`
	// FadeMs 是切换的过渡时长（缺省 240ms）。0 表示硬切，用于「不需要喘口气」的场景。
	FadeMs int64 `json:"fadeMs,omitempty"`
}

// Layer 是一个可做元素级动作的区域。ElementID 为空表示整页图层。
type Layer struct {
	ElementID string   `json:"elementId,omitempty"`
	Kind      string   `json:"kind"`
	AssetMode string   `json:"assetMode"`
	Box       Box      `json:"box"`
	Clips     []Motion `json:"clips"`
	// Origin 是缩放的锚点（center/bottom/top/left/right，缺省 center）。
	// 为什么必须能指定：一根柱子锚在中心做 scaleY，会同时向上长、向下长穿基线，
	// 看起来像「柱子从中间冒出来」；锚底才是「从基线长上去」。
	// 它只影响 transform-origin，不改任何属性轨道——同一份 clips 换个锚点即可。
	Origin string `json:"origin,omitempty"`
	// Image 是本层「只含自己」的位图（相对工作目录）。为空时合成器退回
	// 「从 baseImage 按 Box 偏移裁切」的旧行为。
	//
	// 为什么需要它：元素 A 的 Box 若包含另一个动画元素 B，从 baseImage 裁出来的
	// A 里就烘着 B，B 会跟着 A 的不透明度出现（ADR-9）。backend 为每层生成一张
	// 「把其它动画元素抹掉」的图，合成器直出即可。
	Image string `json:"image,omitempty"`
}

// Box 是像素矩形（整数像素：半像素会让 1080p 下的描边发虚）。
type Box struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// 缩放锚点的取值集合。空串等价于 OriginCenter（兼容旧计划）。
const (
	OriginCenter = "center"
	OriginBottom = "bottom"
	OriginTop    = "top"
	OriginLeft   = "left"
	OriginRight  = "right"
)

// ValidOrigin 报告锚点是否在白名单内。
func ValidOrigin(origin string) bool {
	switch origin {
	case "", OriginCenter, OriginBottom, OriginTop, OriginLeft, OriginRight:
		return true
	}
	return false
}

// Motion 是一条已展开的动效。
type Motion struct {
	ClipID string `json:"clipId,omitempty"`
	// Effect 只作审计留痕（哪条规格生成了这一段）。渲染后端 MUST NOT 读它决定行为。
	Effect     string                 `json:"effect"`
	StartMs    int64                  `json:"startMs"`
	DurationMs int64                  `json:"durationMs"`
	Easing     animation.CubicBezier  `json:"easing"`
	Props      []animation.MotionProp `json:"props"`
}

// Transition 是页间转场。挂在后一页：它描述"如何进入这一页"。
type Transition struct {
	Type       string `json:"type"` // none|fade|slide|zoom
	DurationMs int64  `json:"durationMs,omitempty"`
}

// 诊断码。诊断不阻断渲染，但必须留痕：静默修正比报错更难排查。
const (
	DiagAnchorMissing         = "anchor_missing"
	DiagBoxClamped            = "box_clamped"
	DiagSegmentUnresolved     = "segment_unresolved"
	DiagDurationTierConflict  = "duration_tier_conflict"
	DiagGeometryInferred      = "geometry_inferred"
	DiagClipTruncated         = "clip_truncated"
	DiagTransitionDefaulted   = "transition_defaulted"
	DiagSiblingOverlapPartial = "sibling_overlap_partial"
)

// Diagnostic 是一条编译诊断。
type Diagnostic struct {
	Code      string `json:"code"`
	Page      int    `json:"page"`
	ElementID string `json:"elementId,omitempty"`
	ClipID    string `json:"clipId,omitempty"`
	Message   string `json:"message"`
}

// FrameMS 返回一帧的毫秒数（向上取整），与 animation.PageContext.FrameMS 同口径。
func (t *Timeline) FrameMS() int64 {
	fps := t.FPS
	if fps <= 0 {
		fps = 30
	}
	return int64(math.Ceil(1000.0 / float64(fps)))
}

// TotalDurationMs 返回全片时长。
func (t *Timeline) TotalDurationMs() int64 {
	var total int64
	for _, p := range t.Pages {
		total += p.DurationMs
	}
	return total
}

// SegmentName 是第 i 页片段在产物目录里的文件名。
//
// 这是服务端与调用方之间的约定，之前只以字面量形式散在后端实现里：
// 调用方按下标算不出同一个名字，就只能靠「产物列表里挑看起来像 mp4 的」——
// 那在产物多起来（未来可能附带封面、音频）时会挑错。
func SegmentName(i int) string { return fmt.Sprintf("segment_%03d.mp4", i) }
