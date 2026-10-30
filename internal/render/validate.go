package render

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"

	"github.com/F31/ppts/internal/animation"
)

// PlanError 是 IR 校验失败。Field 是可定位的 JSON 路径（如 pages[2].layers[0].box.w），
// 让调用方不必靠猜去寻找出错的字段。
type PlanError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *PlanError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("render: %s at %s: %s", e.Code, e.Field, e.Message)
	}
	return fmt.Sprintf("render: %s: %s", e.Code, e.Message)
}

func planErr(field, code, format string, args ...any) *PlanError {
	return &PlanError{Field: field, Code: code, Message: fmt.Sprintf(format, args...)}
}

// 校验错误码。
const (
	CodeInvalidPlan = "invalid_plan"
	CodeUnsafePath  = "unsafe_path"
	CodeUnsupported = "unsupported"
)

// Validate 校验 IR 的全部不变量。它只做"渲染器能否安全执行"这一层判断：
// 规格本身是否合法由 animation.Validate 负责，两者不重复。
func (t *Timeline) Validate() error {
	if t == nil {
		return planErr("", CodeInvalidPlan, "plan is nil")
	}
	if t.SchemaVersion != IRSchemaVersion {
		return planErr("schemaVersion", CodeUnsupported, "want %q, got %q", IRSchemaVersion, t.SchemaVersion)
	}
	if t.FPS < MinFPS || t.FPS > MaxFPS {
		return planErr("fps", CodeInvalidPlan, "%d outside [%d,%d]", t.FPS, MinFPS, MaxFPS)
	}
	if t.Width <= 0 || t.Height <= 0 {
		return planErr("width", CodeInvalidPlan, "canvas %dx%d must be positive", t.Width, t.Height)
	}
	// H.264 的 yuv420p 要求偶数尺寸；奇数会在编码阶段被 ffmpeg 拒绝或补边。
	if t.Width%2 != 0 || t.Height%2 != 0 {
		return planErr("width", CodeInvalidPlan, "canvas %dx%d must be even (yuv420p)", t.Width, t.Height)
	}
	if strings.TrimSpace(t.Background) == "" {
		return planErr("background", CodeInvalidPlan, "background color is required")
	}
	if len(t.Pages) == 0 {
		return planErr("pages", CodeInvalidPlan, "plan has no pages")
	}
	if len(t.Pages) > MaxPages {
		return planErr("pages", CodeInvalidPlan, "%d pages exceeds limit %d", len(t.Pages), MaxPages)
	}

	frame := t.FrameMS()
	for i := range t.Pages {
		if err := t.validatePage(&t.Pages[i], frame); err != nil {
			return err
		}
	}
	return nil
}

func (t *Timeline) validatePage(p *Page, frameMS int64) error {
	base := fmt.Sprintf("pages[%d]", p.Index)
	if p.Index < 0 {
		return planErr(base+".index", CodeInvalidPlan, "negative index")
	}
	if strings.TrimSpace(p.SlideID) == "" {
		return planErr(base+".slideId", CodeInvalidPlan, "slideId is required")
	}
	if p.DurationMs <= 0 {
		return planErr(base+".durationMs", CodeInvalidPlan, "duration must be positive (got %d)", p.DurationMs)
	}
	if err := SafeAssetPath(p.BaseImage); err != nil {
		return planErr(base+".baseImage", CodeUnsafePath, "%s", err)
	}
	// 底板可选（没有元素级图层时不存在），但一旦出现就必须同样安全：
	// 它是后端生成的文件名，却也来自计划字节，不能因为是「派生物」就放宽。
	if p.PlateImage != "" {
		if err := SafeAssetPath(p.PlateImage); err != nil {
			return planErr(base+".plateImage", CodeUnsafePath, "%s", err)
		}
	}
	if err := validateTransition(p.Transition); err != nil {
		return planErr(base+".transition", CodeInvalidPlan, "%s", err)
	}

	for li := range p.Layers {
		l := &p.Layers[li]
		lbase := fmt.Sprintf("%s.layers[%d]", base, li)
		if !validKind(l.Kind) {
			return planErr(lbase+".kind", CodeInvalidPlan, "unknown kind %q", l.Kind)
		}
		switch l.AssetMode {
		case AssetBitmapRegion, AssetPageLayer:
		default:
			return planErr(lbase+".assetMode", CodeUnsupported, "unsupported assetMode %q", l.AssetMode)
		}
		if l.Box.W <= 0 || l.Box.H <= 0 {
			return planErr(lbase+".box", CodeInvalidPlan, "box %dx%d must be positive", l.Box.W, l.Box.H)
		}
		// 锚点写错不会报错、只会「长得不对」，是最难查的一类：
		// 合成器会把未知值当默认，柱子就从中间长出来，而一切看起来都正常。
		if !ValidOrigin(l.Origin) {
			return planErr(lbase+".origin", CodeInvalidPlan,
				"unknown origin %q (want center|bottom|top|left|right)", l.Origin)
		}
		// 层素材是后端生成的派生物，但同样来自计划字节，路径安全不能放宽。
		if l.Image != "" {
			if err := SafeAssetPath(l.Image); err != nil {
				return planErr(lbase+".image", CodeUnsafePath, "%s", err)
			}
			// page-layer 就是整页本身，给它一张位图没有意义，只会掩盖调用方搞错了对象。
			if l.AssetMode != AssetBitmapRegion {
				return planErr(lbase+".image", CodeInvalidPlan,
					"only %s layers may carry image", AssetBitmapRegion)
			}
		}
		if l.Box.X < 0 || l.Box.Y < 0 ||
			l.Box.X+l.Box.W > t.Width || l.Box.Y+l.Box.H > t.Height {
			return planErr(lbase+".box", CodeInvalidPlan,
				"box (%d,%d %dx%d) outside canvas %dx%d", l.Box.X, l.Box.Y, l.Box.W, l.Box.H, t.Width, t.Height)
		}
		for ci := range l.Clips {
			if err := validateMotion(&l.Clips[ci], p.DurationMs, frameMS,
				fmt.Sprintf("%s.clips[%d]", lbase, ci)); err != nil {
				return err
			}
		}
	}
	if err := validateFocus(p, base); err != nil {
		return err
	}
	return nil
}

// validateFocus 校验页级焦点序列。
//
// 三条必须成立：焦点指向本页真实存在的图层（否则「让位」没有对象）、
// 起点严格递增（同一时刻两个焦点是自相矛盾的状态）、不越过页长
// （越过意味着后半页没有焦点，非焦点元素会一直压暗着播到页尾）。
func validateFocus(p *Page, base string) error {
	layers := make(map[string]struct{}, len(p.Layers))
	for _, l := range p.Layers {
		if l.ElementID != "" {
			layers[l.ElementID] = struct{}{}
		}
	}
	prev := int64(-1)
	seen := make(map[string]struct{}, len(p.Focus))
	for i, f := range p.Focus {
		fb := fmt.Sprintf("%s.focus[%d]", base, i)
		if _, ok := layers[f.ElementID]; !ok {
			return planErr(fb+".elementId", CodeInvalidPlan,
				"focus element %q is not a layer of this page", f.ElementID)
		}
		if _, dup := seen[f.ElementID]; dup {
			// 求值器按 elementId 查压暗量，「甲又亮又暗」无法表达。
			return planErr(fb+".elementId", CodeInvalidPlan,
				"focus element %q appears twice", f.ElementID)
		}
		seen[f.ElementID] = struct{}{}
		if f.StartMs < 0 {
			return planErr(fb+".startMs", CodeInvalidPlan, "negative start %d", f.StartMs)
		}
		if f.StartMs >= p.DurationMs {
			return planErr(fb+".startMs", CodeInvalidPlan,
				"focus starts at %d, page is %dms", f.StartMs, p.DurationMs)
		}
		if f.StartMs <= prev {
			return planErr(fb+".startMs", CodeInvalidPlan,
				"focus starts must be strictly increasing (%d after %d)", f.StartMs, prev)
		}
		if f.FadeMs < 0 || f.FadeMs > MaxFocusFadeMS {
			return planErr(fb+".fadeMs", CodeInvalidPlan,
				"fade %d outside [0,%d]", f.FadeMs, MaxFocusFadeMS)
		}
		// EndMs=0 是「不放手」，不是「在 0ms 放手」：0 与「没写」在 JSON 里是同一个
		// 字节（omitempty），所以终点只能表达成非零值。要是有人真写 0，落到这里报错
		// 比让它静默变成「保持到下一段」好——那正是这条字段要修的病。
		if f.EndMs != 0 {
			if f.EndMs < 0 {
				return planErr(fb+".endMs", CodeInvalidPlan, "negative end %d", f.EndMs)
			}
			if f.EndMs <= f.StartMs {
				return planErr(fb+".endMs", CodeInvalidPlan,
					"focus ends at %d, starts at %d", f.EndMs, f.StartMs)
			}
			if f.EndMs > p.DurationMs {
				return planErr(fb+".endMs", CodeInvalidPlan,
					"focus ends at %d, page is %dms", f.EndMs, p.DurationMs)
			}
			// 盖住下一段等于「同时指两个对象」，求值器只会认最后一段，前一段的终点
			// 静默失效——编排以为放掉了，画面没放。
			if i+1 < len(p.Focus) && f.EndMs > p.Focus[i+1].StartMs {
				return planErr(fb+".endMs", CodeInvalidPlan,
					"focus ends at %d, after next focus starts at %d",
					f.EndMs, p.Focus[i+1].StartMs)
			}
		}
		prev = f.StartMs
	}
	return nil
}

func validateMotion(m *Motion, pageMS, frameMS int64, field string) error {
	if m.StartMs < 0 {
		return planErr(field+".startMs", CodeInvalidPlan, "negative start %d", m.StartMs)
	}
	if m.DurationMs < 1 {
		return planErr(field+".durationMs", CodeInvalidPlan, "duration must be ≥1ms (got %d)", m.DurationMs)
	}
	// 与 V3 同口径：允许 1 帧容差，但渲染阶段不再负责裁剪（裁剪意味着画面与规格不一致）。
	if m.StartMs+m.DurationMs > pageMS+frameMS {
		return planErr(field, CodeInvalidPlan,
			"clip [%d,%d) exceeds page duration %d", m.StartMs, m.StartMs+m.DurationMs, pageMS)
	}
	if len(m.Props) == 0 {
		return planErr(field+".props", CodeInvalidPlan, "motion has no properties")
	}
	if !validEasing(m.Easing) {
		return planErr(field+".easing", CodeInvalidPlan,
			"control points (%.3f,%.3f,%.3f,%.3f) invalid", m.Easing.X1, m.Easing.Y1, m.Easing.X2, m.Easing.Y2)
	}
	for pi := range m.Props {
		prop := &m.Props[pi]
		if !animation.ValidProp(prop.Name) {
			return planErr(fmt.Sprintf("%s.props[%d].name", field, pi), CodeUnsupported,
				"unknown property %q", prop.Name)
		}
		if math.IsNaN(prop.From) || math.IsNaN(prop.To) ||
			math.IsInf(prop.From, 0) || math.IsInf(prop.To, 0) {
			return planErr(fmt.Sprintf("%s.props[%d]", field, pi), CodeInvalidPlan,
				"non-finite value (%v → %v)", prop.From, prop.To)
		}
	}
	return nil
}

// validEasing 校验 cubic-bezier 控制点：x 必须在 [0,1]（CSS 规范），
// y 允许越界——回弹的过冲正是靠 y>1 表达的，上限给 3 已经很宽松。
func validEasing(e animation.CubicBezier) bool {
	if e.X1 < 0 || e.X1 > 1 || e.X2 < 0 || e.X2 > 1 {
		return false
	}
	if e.Y1 < -1 || e.Y1 > 3 || e.Y2 < -1 || e.Y2 > 3 {
		return false
	}
	return true
}

func validateTransition(t Transition) error {
	switch t.Type {
	case animation.TransitionNone:
		if t.DurationMs != 0 {
			return fmt.Errorf("transition %q must have durationMs=0", t.Type)
		}
	case animation.TransitionFade, animation.TransitionSlide, animation.TransitionZoom:
		if t.DurationMs <= 0 || t.DurationMs > animation.MaxTransitionMS {
			return fmt.Errorf("transition %q duration %d outside (0,%d]", t.Type, t.DurationMs, animation.MaxTransitionMS)
		}
	default:
		return fmt.Errorf("unknown transition type %q", t.Type)
	}
	return nil
}

func validKind(kind string) bool {
	switch kind {
	case animation.KindPage, animation.KindTitle, animation.KindBullet, animation.KindImage,
		animation.KindIcon, animation.KindChart, animation.KindTable, animation.KindShape:
		return true
	}
	return false
}

// SafeAssetPath 拒绝任何可能逃出工作根目录的路径。渲染服务运行在不信任输入下（§8.7）：
// 计划来自 API，而 API 的数据来自用户上传的 PPT 解析结果。
//
// 规则：非空、相对路径、正斜杠、不含 ..、不含盘符/冒号。
// 反斜杠直接拒绝而不是规范化——IR 由本包生成，出现反斜杠说明有人在手工构造计划。
func SafeAssetPath(p string) error {
	if strings.TrimSpace(p) == "" {
		return fmt.Errorf("asset path is empty")
	}
	if strings.ContainsAny(p, "\\:") {
		return fmt.Errorf("asset path %q contains backslash or drive prefix", p)
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("asset path %q is absolute", p)
	}
	if filepath.IsAbs(p) {
		return fmt.Errorf("asset path %q is absolute", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return fmt.Errorf("asset path %q escapes the work root", p)
		}
	}
	return nil
}
