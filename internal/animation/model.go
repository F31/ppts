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

// 绑定来源标注（Q7 决策 4：每条机器绑定 MUST 带来源）。
//
// 七个规则来源与 `scripts/fidelity/bind_elements.py` 的报告口径一一对应——移植时不合并
// 任何两类：实验里「同块带出」的错是成块的（一列四件一起错），按来源一筛就能发现；
// 字面匹配的错是零散的，只能逐条看。合并来源等于把复核成本从 O(最可疑的一类) 抬回 O(全部)。
//
// manual 表示人改过。它存在的意义是**否定**：一条 clip 一旦标成 manual，就不能再被
// 预填逻辑当作「机器结论」批量撤销或覆盖。
const (
	TriggerSourceText       = "text"       // 段落 ↔ 元素字面相似度（单调 DP）
	TriggerSourceCount      = "count"      // 数量词 → 编号组（「五个部分」）
	TriggerSourceOrdinal    = "ordinal"    // 序数 → 第 N 个编号块（「第二条」）
	TriggerSourceSpatial    = "spatial"    // 方位指代 → 包围盒位置（「右边那张图」）
	TriggerSourceSibling    = "sibling"    // 同块带出（与已绑元素同卡片/紧邻）
	TriggerSourcePrior      = "prior"      // 标题先验（页内首个标题 → 第一段）
	TriggerSourceStructural = "structural" // 结构推断（容器跟着装着的内容走）
	TriggerSourceManual     = "manual"     // 人工编辑/确认
)

// triggerSources 是白名单的**顺序**声明：错误信息里按这个顺序列出允许取值，
// 顺序即「越靠前越可信」的排序，编辑器的默认呈现顺序也用它。
var triggerSources = []string{
	TriggerSourceText, TriggerSourceCount, TriggerSourceOrdinal,
	TriggerSourceSpatial, TriggerSourceSibling, TriggerSourcePrior,
	TriggerSourceStructural, TriggerSourceManual,
}

// TriggerSources 返回全部允许的来源标注（拷贝，调用方可随意修改）。
func TriggerSources() []string {
	out := make([]string, len(triggerSources))
	copy(out, triggerSources)
	return out
}

// ValidTriggerSource 报告 s 是否为合法来源标注。空串表示「无标注」，是合法缺省值。
func ValidTriggerSource(s string) bool {
	if s == "" {
		return true
	}
	for _, v := range triggerSources {
		if v == s {
			return true
		}
	}
	return false
}

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
	Shots            []ShotSpec `json:"shots,omitempty"` // 页内镜头切片（见 render.PageShotInput）
	UpdatedAt        time.Time
}

// ShotBox 是镜头裁切框（源页 EMU 坐标）。nil 表示整页（首镜）。
type ShotBox struct {
	X      int64 `json:"x"`
	Y      int64 `json:"y"`
	Width  int64 `json:"width"`
	Height int64 `json:"height"`
}

// ShotSpec 是一段页内镜头：时序 + 取自原页的裁切框（EMU）。
//
// 坐标与 Track 的元素几何同宗（都是源页 EMU），由 render.Compiler 统一换算成画布像素。
// 首镜（index 0）就是整页，Crop 留空；CrossFadeMs 缺省由编排侧补 120。
type ShotSpec struct {
	StartMs     int64    `json:"startMs"`
	DurationMs  int64    `json:"durationMs"`
	CrossFadeMs int64    `json:"crossFadeMs,omitempty"`
	Crop        *ShotBox `json:"crop,omitempty"`
	// Camera 是镜头内的相机运动关键帧（归一化坐标）。仅用于带 crop 的特写；首镜 MUST 为空。
	// 与 render.CameraKeyframe 同构，revShots 原样搬到渲染输入。
	Camera []CameraKeyframe `json:"camera,omitempty"`
}

// CameraKeyframe 是镜头图局部的归一化相机关键帧（作者层坐标，与 render 同语义）。
// 详见 render.CameraKeyframe 注释：x/y 视口中心（归一化到镜头图）；合法值 MUST 落在覆盖安全
// 区间 [0.5/scale,1-0.5/scale]（由校验与运行时 clamp 保证），scale ≥ 1 相对「图覆盖舞台」。
type CameraKeyframe struct {
	AtMs  int64   `json:"atMs"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Scale float64 `json:"scale"`
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
	// Continues 表示本 clip 允许跨页延续到下一页（G8 跨场景元素延续）。为 true 时，
	// 编译期在页边界拆分（见 render-plan），保存期允许 startMs+durationMs 超出页时长；
	// 为 false（缺省）时行为不变。跨页身份复用 Track.ElementID。omitempty 保证缺省值
	// 不进序列化，避免改变既有规格的字节/哈希。
	Continues bool `json:"continues,omitempty"`
	// TriggerSource 标注这条 clip 是怎么来的（规则名 / manual）。空串 = 无标注，
	// 表示「不是预填产物」（手写规格、旧客户端保存的规格都落在这一档）。
	//
	// omitempty 是字节不变的前提：既有规格的序列化与 planHash 不因为多了一个字段而变。
	TriggerSource string `json:"triggerSource,omitempty"`
	// Confidence 是预填给这条绑定的连续置信度（0..1），编辑器按升序呈现，让用户先看最可疑的。
	//
	// 0 有两个含义，都表示「没有可用的置信度」：非预填产物（手写/旧规格）与
	// 非字面规则（数量词、序数、方位、同块、先验、结构）——后者本来就没有字面依据，
	// 硬塞一个数进去只会让人误以为可以互相排序。
	Confidence float64 `json:"confidence,omitempty"`
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
	// Segments 是分段 id + 文本，与 SegmentIDs 同序。可空（还没写讲稿）。
	//
	// 校验只要 id，文本只有「讲稿绑定预填」用得上：预填靠段落文本与元素文本的
	// 相似度决定顺序，没有文本就只剩几何规则。把它放在这里，是因为解析页上下文时
	// 讲稿已经在手上——再单开一次读取，只会让预填与校验看到两份可能不同的分段。
	Segments []ScriptSegment
}

// ScriptSegment 是讲稿分段在页上下文里的最小视图。ID 必须与 SegmentIDs 一致。
type ScriptSegment struct {
	ID   string
	Text string
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
