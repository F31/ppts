// Package animation 定义单页动画规格（AnimationSpec）：数据结构、动效白名单、
// 服务端权威校验与默认规格生成。
//
// 分层约束（设计方案 §4.2）：本包属于 L1 规格层，不得依赖 internal/media、
// internal/app、internal/api。页面时长、讲稿分段 id、元素锚点等运行时上下文
// 一律由调用方通过 PageContext 注入，本包不感知时间轴实现与渲染后端。
package animation

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
)

// SchemaVersion 是规格 JSON 契约版本（设计方案 §5.5）。
const SchemaVersion = "animation/v1"

// 规格规模与取值边界（设计方案 §5.4 V5/V11/V12）。改这些值等于改契约。
const (
	MaxTracksPerSlide = 30
	MaxClipsPerSlide  = 40
	MaxSpecBytes      = 256 * 1024

	MinClipDurationMS = 120
	MaxClipDurationMS = 3000

	MinIntensity = 0.5
	MaxIntensity = 1.5
)

// Status 是规格审批状态，沿用讲稿状态机（draft → approved → locked，locked 可回 approved）。
type Status string

const (
	StatusDraft    Status = "draft"
	StatusApproved Status = "approved"
	StatusLocked   Status = "locked"
)

// 元素轨道类型（设计方案 §5.2 Track.Kind）；"page" 表示整页轨道。
const (
	KindPage   = "page"
	KindTitle  = "title"
	KindBullet = "bullet"
	KindImage  = "image"
	KindIcon   = "icon"
	KindChart  = "chart"
	KindTable  = "table"
	KindShape  = "shape"
)

// 动效类别（设计方案 §5.4 V4：入场/退场各最多一条，强调可多条）。
const (
	ClassEntrance = "entrance"
	ClassEmphasis = "emphasis"
	ClassExit     = "exit"
)

// 三种触发锚定形式（设计方案 §5.3）。
//
// 导出是因为默认规格生成在子包 animdefault：那里要用同一批字面量拼 trigger，
// 若各写一份字符串，校验器改了前缀而生成器没跟，就会产出自己都不接受的规格。
const (
	TriggerEnter         = "onEnter"
	TriggerSegmentPrefix = "withSegment:"
	TriggerAtTimePrefix  = "atTime:"
)

// 允许的缓动曲线。
var validEasings = map[string]struct{}{
	"linear":    {},
	"easeOut":   {},
	"easeInOut": {},
	"spring":    {},
}

// Key 是规格的隔离维度：源版本 0 表示 legacy（与讲稿同规则）。
type Key struct {
	TenantID         string
	ProjectID        string
	SlideID          string
	Language         string
	SourceRevisionNo int
}

// Revision 是单页动画规格，与 narration.Revision 同构：源版本隔离、乐观并发、
// 审批锁定、渲染失效标记。
type Revision struct {
	ID               string
	TenantID         string
	ProjectID        string
	SlideID          string
	Language         string
	SourceRevisionNo int   // 0 = legacy（与讲稿同规则）
	Revision         int64 // 乐观并发版本
	Status           Status
	PresetID         string  // 页面级预设；空串表示自定义（不套预设）
	Intensity        float64 // 统一缩放幅度/时长，[MinIntensity, MaxIntensity]
	Tracks           []*Track
	RenderedRevision int64 // 最近一次成功渲染对应的 Revision；< Revision 表示过期
	UpdatedAt        time.Time
}

// Track 是一个可视元素上的动效集合。ElementID 为空表示整页轨道（Kind 必须为 page）。
type Track struct {
	ElementID string  `json:"elementId"` // project.Shape.ID；"" 表示页面级
	Kind      string  `json:"kind"`      // title|bullet|image|shape|icon|chart|table|page
	Clips     []*Clip `json:"clips"`
}

// Clip 是单条动效片段。时间一律相对本页起点，单位毫秒（1080p 逻辑量，见设计方案 §5.2）。
type Clip struct {
	ID         string `json:"id,omitempty"`
	Type       string `json:"type"` // catalog 白名单
	StartMs    int64  `json:"startMs"`
	DurationMs int64  `json:"durationMs"`
	Easing     string `json:"easing"` // linear|easeOut|easeInOut|spring
	Params     Params `json:"params,omitempty"`
	Trigger    string `json:"trigger"` // onEnter | withSegment:<id> | atTime:<ms>
}

// Params 是动效参数。字段名与 JSON key 一一对应；未登记的 key 一律拒收，
// 避免"写了但被静默忽略"的参数（设计方案 §6.4 的 catalog 契约）。
type Params struct {
	Direction string  `json:"direction,omitempty"` // up|down|left|right
	Offset    float64 `json:"offset,omitempty"`    // 1080p 基准像素
	ScaleFrom float64 `json:"scaleFrom,omitempty"`
	Axis      string  `json:"axis,omitempty"` // x|y
	Loop      bool    `json:"loop,omitempty"`
}

// UnmarshalJSON 拒绝未知参数 key。错误在解码期暴露，而不是等渲染时才被忽略。
func (p *Params) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("animation: params must be an object: %w", err)
	}
	*p = Params{}
	for key, val := range raw {
		var err error
		switch key {
		case "direction":
			err = json.Unmarshal(val, &p.Direction)
		case "offset":
			err = json.Unmarshal(val, &p.Offset)
		case "scaleFrom":
			err = json.Unmarshal(val, &p.ScaleFrom)
		case "axis":
			err = json.Unmarshal(val, &p.Axis)
		case "loop":
			err = json.Unmarshal(val, &p.Loop)
		default:
			return fmt.Errorf("animation: unknown param %q", key)
		}
		if err != nil {
			return fmt.Errorf("animation: param %q: %w", key, err)
		}
	}
	return nil
}

// IsZero 报告参数是否全部为默认值。
func (p Params) IsZero() bool {
	return p == Params{}
}

// Anchor 是页内可挂载动画的元素（由调用方从 project.Shape 扁平化而来）。
type Anchor struct {
	ID        string
	Kind      string
	HasBounds bool // 无包围盒的元素不做元素级动画（设计方案 §5.2）
}

// PageContext 是校验与默认生成所需的运行时上下文。它刻意不引用时间轴类型：
// 页时长、fps、分段 id 由编排层换算后注入，保证 L1 不反向依赖 L2/L3。
type PageContext struct {
	DurationMS int64    // 页时长（Timeline 的 EndUS-StartUS，毫秒）
	FPS        int      // 用于 1 帧容差；<=0 时按 30 处理
	Elements   []Anchor // 页内可挂载元素（含 Children 递归）
	SegmentIDs []string // 当前讲稿修订的分段 id（V7）
}

// FrameMS 返回一帧的毫秒数（向上取整），用于 V3 的 1 帧容差。
func (pc PageContext) FrameMS() int64 {
	fps := pc.FPS
	if fps <= 0 {
		fps = 30
	}
	return int64(math.Ceil(1000.0 / float64(fps)))
}

// ErrNotFound 表示该隔离维度下不存在规格。
var ErrNotFound = errors.New("animation: spec not found")

// ErrLocked 表示规格已锁定，禁止写入（V9）。
var ErrLocked = errors.New("animation: spec is locked")

// ErrConflict 表示 expected_revision 与最新版本不一致（V10）；Latest 携带最新版供对比。
type ErrConflict struct {
	Latest *Revision
}

func (e *ErrConflict) Error() string { return "animation: revision conflict" }

// ValidationError 是一条规则违反的明细。Rule 对应设计方案 §5.4 的编号（V1–V12），
// Status 是建议的 HTTP 状态（400/409/413），由接入层直接映射，避免各处重复判断。
type ValidationError struct {
	Rule    string
	Field   string
	Message string
	Hint    string
	Status  int
}

func (e *ValidationError) Error() string {
	if e.Field == "" {
		return fmt.Sprintf("animation: %s: %s", e.Rule, e.Message)
	}
	return fmt.Sprintf("animation: %s %s: %s", e.Rule, e.Field, e.Message)
}

// CheckWritable 实现 V9/V10 的纯函数部分：接入层应当在校验内容之前调用它，
// 先回答"你能不能写"，再回答"你写错了什么"（设计方案 §5.4 的判定顺序）。
func (r *Revision) CheckWritable(expected int64) error {
	if r.Status == StatusLocked {
		return ErrLocked
	}
	if r.Revision != expected {
		return &ErrConflict{Latest: r}
	}
	return nil
}

// ClipCount 返回全页 clip 总数（V11）。nil 轨道/片段不计，交由 Validate 判为结构错误。
func (r *Revision) ClipCount() int {
	n := 0
	for _, t := range r.Tracks {
		if t == nil {
			continue
		}
		n += len(t.Clips)
	}
	return n
}

// Size 返回规格的可持久化字节数（V12）。只统计会落库的内容，不含 ID/租户/时间戳。
func (r *Revision) Size() int {
	payload := struct {
		PresetID  string   `json:"presetId"`
		Intensity float64  `json:"intensity"`
		Tracks    []*Track `json:"tracks"`
	}{PresetID: r.PresetID, Intensity: r.Intensity, Tracks: r.Tracks}
	b, err := json.Marshal(payload)
	if err != nil { // 结构本身可序列化，这里只可能是极端情况
		return 0
	}
	return len(b)
}
