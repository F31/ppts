package render

import (
	"errors"
	"fmt"
	"math"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/design"
)

// defaultSlideHeightEMU 是 PowerPoint 标准页高（7.5 英寸）。
// 16:9（12192000）与 4:3（9144000）都以它为高，因此只要知道底图宽高比就能反推页宽。
const defaultSlideHeightEMU int64 = 6858000

// 编译期哨兵错误。
var (
	// ErrDurationUnknown 表示页时长未知。编辑期允许（V3 跳过上界校验），渲染期不允许：
	// 时长唯一归属时间轴，猜一个时长等于让音画不同步。
	ErrDurationUnknown = errors.New("render: page duration unknown")
	// ErrAspectMismatch 表示传入的页尺寸与画布宽高比不一致。
	ErrAspectMismatch = errors.New("render: slide size does not match canvas aspect")
)

// EMUBox 是源页坐标下的轴对齐矩形（EMU）。
//
// 刻意不复用 project.Box：那会把 internal/project 连同 internal/db（sqlite 驱动）
// 一起链接进渲染服务二进制。渲染镜像不需要数据库，也不该背它。
// 调用方做一次 3 个字段的拷贝，换来 L4 的干净依赖面。
type EMUBox struct {
	X, Y, Width, Height int64
}

// Element 是页内一个可挂载动画的元素（EMU 几何）。
type Element struct {
	ID     string
	Kind   string
	Bounds *EMUBox
}

// PageInput 是一页的编译输入。它刻意不引用时间轴类型：分段偏移由调用方换算后注入，
// 与 animation.PageContext 的取向一致（L1/L3 都不知道 L2 的时间轴实现）。
type PageInput struct {
	Index      int
	SlideID    string
	DurationMs int64
	BaseImage  string
	Elements   []Element
	Revision   *animation.Revision
	// SegmentOffsets 是分段 id → 相对本页起点的毫秒偏移。
	SegmentOffsets map[string]int64
}

// Options 是编译器的环境参数。
type Options struct {
	Canvas design.Canvas
	// SlideWidthEMU/SlideHeightEMU 为 0 时按底图宽高比推断（高度固定 6858000）。
	SlideWidthEMU  int64
	SlideHeightEMU int64
	FPS            int
	Background     string
	Fonts          []Font
}

// Compiler 把规格编译成 IR。构造后不可变，可并发使用。
type Compiler struct {
	opt Options
	// scale 是 EMU → 像素的等比系数。X/Y 共用：两轴各自缩放会让圆变椭圆。
	scale      float64
	geometryOK bool // false 表示页尺寸是推断的，需要打诊断
	baselineK  float64
	motion     design.MotionProfile
}

// NewCompiler 校验环境参数并预计算换算系数。
func NewCompiler(opt Options) (*Compiler, error) {
	if opt.Canvas.Width <= 0 || opt.Canvas.Height <= 0 {
		return nil, fmt.Errorf("render: canvas %dx%d must be positive", opt.Canvas.Width, opt.Canvas.Height)
	}
	if opt.FPS == 0 {
		opt.FPS = 30
	}
	if opt.FPS < MinFPS || opt.FPS > MaxFPS {
		return nil, fmt.Errorf("render: fps %d outside [%d,%d]", opt.FPS, MinFPS, MaxFPS)
	}
	if opt.Background == "" {
		opt.Background = "#FFFFFF"
	}

	inferred := false
	h := opt.SlideHeightEMU
	w := opt.SlideWidthEMU
	if h <= 0 {
		h = defaultSlideHeightEMU
		inferred = true
	}
	if w <= 0 {
		// 底图是按源页比例渲染的，所以"底图宽高比"就是"页宽高比"。
		w = int64(math.Round(float64(h) * opt.Canvas.Aspect()))
		inferred = true
	} else {
		got := float64(w) / float64(h)
		want := opt.Canvas.Aspect()
		if math.Abs(got-want)/want > 0.005 {
			return nil, fmt.Errorf("%w: slide %dx%d EMU (%.4f) vs canvas %.4f",
				ErrAspectMismatch, w, h, got, want)
		}
	}
	return &Compiler{
		opt:        opt,
		scale:      float64(opt.Canvas.Height) / float64(h),
		geometryOK: !inferred,
		baselineK:  float64(opt.Canvas.Height) / float64(animation.BaselineHeight),
		motion:     design.DefaultMotion(),
	}, nil
}

// Options 返回生效后的环境参数（已补齐缺省值）。
func (c *Compiler) Options() Options { return c.opt }

// Compile 编译整片。
func (c *Compiler) Compile(pages []PageInput) (*Timeline, error) {
	if len(pages) == 0 {
		return nil, errors.New("render: no pages to compile")
	}
	if len(pages) > MaxPages {
		return nil, fmt.Errorf("render: %d pages exceeds limit %d", len(pages), MaxPages)
	}
	out := &Timeline{
		SchemaVersion: IRSchemaVersion,
		FPS:           c.opt.FPS,
		Width:         c.opt.Canvas.Width,
		Height:        c.opt.Canvas.Height,
		Background:    c.opt.Background,
		Fonts:         c.opt.Fonts,
		Pages:         make([]Page, 0, len(pages)),
	}
	for i, in := range pages {
		// 页序以调用方给的顺序为准：IR 的 index 是播放顺序，不能出现空洞或重复。
		in.Index = i
		page, diags, err := c.CompilePage(in)
		if err != nil {
			return nil, err
		}
		out.Pages = append(out.Pages, page)
		out.Warnings = append(out.Warnings, diags...)
	}
	if err := out.Validate(); err != nil {
		return nil, err
	}
	return out, nil
}

// CompilePage 编译单页。诊断随页返回：它们不阻断渲染，但必须能被告知。
func (c *Compiler) CompilePage(in PageInput) (Page, []Diagnostic, error) {
	page := Page{
		Index:      in.Index,
		SlideID:    in.SlideID,
		DurationMs: in.DurationMs,
		BaseImage:  in.BaseImage,
		Transition: Transition{Type: animation.TransitionNone},
	}
	if in.DurationMs <= 0 {
		return page, nil, fmt.Errorf("%w: page %d (%s)", ErrDurationUnknown, in.Index, in.SlideID)
	}
	if err := SafeAssetPath(in.BaseImage); err != nil {
		return page, nil, fmt.Errorf("render: page %d: %w", in.Index, err)
	}

	var diags []Diagnostic
	if !c.geometryOK {
		diags = append(diags, Diagnostic{
			Code: DiagGeometryInferred, Page: in.Index,
			Message: "页尺寸未提供，按底图宽高比与标准页高 6858000 EMU 推断",
		})
	}
	if in.Revision != nil {
		tr, d := c.transitionFor(in)
		page.Transition = tr
		if d != nil {
			diags = append(diags, *d)
		}
	}

	elems := make(map[string]Element, len(in.Elements))
	for _, e := range in.Elements {
		elems[e.ID] = e
	}

	if in.Revision != nil {
		intensity := in.Revision.Intensity
		if intensity <= 0 {
			intensity = 1
		}
		for _, track := range in.Revision.Tracks {
			layer, ld, ok := c.compileTrack(in, track, elems, intensity)
			diags = append(diags, ld...)
			if ok {
				page.Layers = append(page.Layers, layer)
			}
		}
		// 顺序是语义的一部分：合成器按数组顺序绘制，而父层位图里的子元素已被抹掉，
		// 父层排在子层之后就会把子元素盖掉（见 layerorder.go）。
		page.Layers = orderLayersParentsFirst(page.Layers)
		// 部分重叠（互不包含）的兄弟元素不在抹除算法的能力范围内：两张层位图会各自
		// 烘进对方落在重叠区的像素，动谁另一个就跟着动（tasks 5.9）。这不是能修掉的
		// 缺陷，但必须留痕——静默的画面错误比报错难查得多。
		if pairs := partialOverlaps(page.Layers); len(pairs) > 0 {
			diags = append(diags, Diagnostic{
				Code: DiagSiblingOverlapPartial, Page: in.Index, ElementID: pairs[0][0],
				Message: fmt.Sprintf("%d 对兄弟元素互不包含但部分重叠（如 %s / %s）："+
					"出片时重叠区可能互相带出（模型限制，见 tasks 5.9）",
					len(pairs), pairs[0][0], pairs[0][1]),
			})
		}
		// 分层冲突按页汇总成一条：catalog 的预设时长（如要点 500ms）普遍落在
		// 视觉基线的"小元素 240–400ms"之外。逐条报会把诊断变成噪音，
		// 而"这一页有 N 条不在档内"既能被看见，又不会淹没真正的异常。
		if n := c.tierConflicts(page.Layers); n > 0 {
			diags = append(diags, Diagnostic{
				Code: DiagDurationTierConflict, Page: in.Index,
				Message: fmt.Sprintf("%d 条 clip 时长不在元素速度分层内（保留 catalog 区间，未改动时长）", n),
			})
		}
	}
	return page, diags, nil
}

// transitionFor 解析本页的入场转场。
//
// 首屏必须为 none：没有前一页可交叉，淡入会让第一帧变成半透明，播放器首帧截图就是灰的。
func (c *Compiler) transitionFor(in PageInput) (Transition, *Diagnostic) {
	if in.Index == 0 {
		return Transition{Type: animation.TransitionNone}, nil
	}
	presetID := in.Revision.PresetID
	t := animation.TransitionFor(presetID)
	out := Transition{Type: t.Type, DurationMs: t.DurationMs}
	if presetID != "" {
		if _, ok := animation.Catalog().Preset(presetID); !ok {
			return out, &Diagnostic{
				Code: DiagTransitionDefaulted, Page: in.Index,
				Message: fmt.Sprintf("预设 %q 不存在，转场回退为 %s", presetID, t.Type),
			}
		}
	}
	return out, nil
}

func (c *Compiler) compileTrack(in PageInput, track *animation.Track, elems map[string]Element,
	intensity float64) (Layer, []Diagnostic, bool) {
	layer := Layer{
		ElementID: track.ElementID,
		Kind:      track.Kind,
		AssetMode: AssetBitmapRegion,
	}
	var diags []Diagnostic

	if track.ElementID == "" {
		// 整页轨道：作用于整张底图（转场之外的整页推拉/淡入）。
		layer.AssetMode = AssetPageLayer
		layer.Box = Box{X: 0, Y: 0, W: c.opt.Canvas.Width, H: c.opt.Canvas.Height}
	} else {
		el, ok := elems[track.ElementID]
		if !ok {
			return layer, []Diagnostic{{
				Code: DiagAnchorMissing, Page: in.Index, ElementID: track.ElementID,
				Message: "元素在当前页不存在，轨道已跳过",
			}}, false
		}
		// 元素自身几何是唯一事实：规格里的 kind 只用于挑预设模板，几何必须来自解析结果。
		if el.Kind != "" {
			layer.Kind = el.Kind
		}
		box, ok, diag := c.toBox(el.Bounds, in.Index, track.ElementID)
		if diag != nil {
			diags = append(diags, *diag)
		}
		if !ok {
			return layer, diags, false
		}
		layer.Box = box
	}

	frame := frameMS(c.opt.FPS)
	for _, clip := range track.Clips {
		m, cd, ok := c.compileClip(in, clip, layer, intensity, frame)
		diags = append(diags, cd...)
		if ok {
			layer.Clips = append(layer.Clips, m)
		}
	}
	if len(layer.Clips) == 0 {
		return layer, diags, false
	}
	return layer, diags, true
}

func (c *Compiler) compileClip(in PageInput, clip *animation.Clip, layer Layer,
	intensity float64, frameMS int64) (Motion, []Diagnostic, bool) {
	m := Motion{ClipID: clip.ID, Effect: clip.Type}
	var diags []Diagnostic

	anchor, ok, diag := resolveTrigger(clip.Trigger, in.SegmentOffsets, in.Index, clip.ID)
	if diag != nil {
		// 分段失效是可恢复的：起点退化为 0 并留痕，用户看到的是"这条提前了"而不是"导出失败"。
		diags = append(diags, *diag)
	}
	if !ok {
		return m, diags, false
	}
	// 触发只给出锚点，startMs 是相对锚点的偏移（设计方案附录 B）。
	// 两者必须相加：只取锚点会让「同一触发器下的错落出场」全部塌缩到 0ms，
	// 用户编排的 700/920/1140 会变成三条同时淡入——表现为"错落丢了"，且没有任何诊断。
	start := anchor + clip.StartMs
	if start < 0 {
		start = 0
	}
	if start >= in.DurationMs {
		diags = append(diags, Diagnostic{Code: DiagClipTruncated, Page: in.Index,
			ElementID: layer.ElementID, ClipID: clip.ID,
			Message: fmt.Sprintf("起点 %dms 超出页长 %dms，clip 已跳过", start, in.DurationMs)})
		return m, diags, false
	}

	duration := clip.DurationMs
	if start+duration > in.DurationMs+frameMS {
		duration = in.DurationMs - start
		if duration < 1 {
			diags = append(diags, Diagnostic{Code: DiagClipTruncated, Page: in.Index,
				ElementID: layer.ElementID, ClipID: clip.ID,
				Message: "clip 无法放进页尾，已跳过"})
			return m, diags, false
		}
	}

	plan, err := animation.PlanMotion(clip.Type, clip.Params, clip.Easing, intensity)
	if err != nil {
		// 未知动效是程序错误（catalog 与 plan.go 不同步），不是用户输入问题。
		diags = append(diags, Diagnostic{Code: "plan_motion_failed", Page: in.Index,
			ElementID: layer.ElementID, ClipID: clip.ID, Message: err.Error()})
		return m, diags, false
	}

	m.StartMs = start
	m.DurationMs = duration
	m.Easing = plan.Curve
	m.Props = make([]animation.MotionProp, 0, len(plan.Props))
	for _, p := range plan.Props {
		// catalog 的位移量以 1080p 为基准，按画布高度等比换算；opacity/scale 无量纲不动。
		if p.Name == animation.PropTranslateX || p.Name == animation.PropTranslateY {
			p.From = math.Round(p.From*c.baselineK*10) / 10
			p.To = math.Round(p.To*c.baselineK*10) / 10
		}
		m.Props = append(m.Props, p)
	}

	if duration != clip.DurationMs {
		diags = append(diags, Diagnostic{Code: DiagClipTruncated, Page: in.Index,
			ElementID: layer.ElementID, ClipID: clip.ID,
			Message: fmt.Sprintf("时长由 %dms 截断为 %dms 以贴合页尾", clip.DurationMs, duration)})
	}
	return m, diags, true
}

// tierTolerance 是速度分层比较的相对容差。
//
// 视觉基线的分层（大 600–900 / 中 400–600 / 小 240–400ms）是"新生成动效"的目标值，
// 而 catalog 预设是已发布的产品默认值，两者本来就不完全重合：business-calm 的标题
// 滑入是 700ms，落在大元素档里合规、落在中元素档里就超了 17%。按严格区间判定，
// 几乎每一页都会带一条警告，真正的异常（图标 2000ms 浮动）反而被淹没。
//
// 因此只报"明显不在档"的：超出上限 25% 或低于下限 25%。
const tierTolerance = 0.25

// tierConflicts 统计明显偏离速度分层的 clip 数。
//
// 只统计、不改时长，理由是"两级契约必须单调"：Validate 接受的规格，Compile 必须能出片。
// bob（1000–3000ms，用于图标）与小元素档（240–400ms）交集为空，而"图标轻微浮动"
// 是合法诉求；若在此硬失败，用户会看到"保存成功、导出失败"——比观感偏差更难解释。
func (c *Compiler) tierConflicts(layers []Layer) int {
	n := 0
	for _, l := range layers {
		area := float64(l.Box.W*l.Box.H) / float64(c.opt.Canvas.Width*c.opt.Canvas.Height)
		r, ok := c.motion.SpeedTiers[c.motion.TierFor(area)]
		if !ok {
			continue
		}
		lo := int64(float64(r.Min) * (1 - tierTolerance))
		hi := int64(float64(r.Max) * (1 + tierTolerance))
		for _, m := range l.Clips {
			if m.DurationMs < lo || m.DurationMs > hi {
				n++
			}
		}
	}
	return n
}

// toBox 把 EMU 包围盒换算为像素并钳制到画布内。
func (c *Compiler) toBox(b *EMUBox, page int, elementID string) (Box, bool, *Diagnostic) {
	if b == nil || b.Width <= 0 || b.Height <= 0 {
		return Box{}, false, &Diagnostic{
			Code: DiagAnchorMissing, Page: page, ElementID: elementID,
			Message: "元素没有可用包围盒（无几何），元素级动画无法挂载",
		}
	}
	x := int(math.Round(float64(b.X) * c.scale))
	y := int(math.Round(float64(b.Y) * c.scale))
	w := int(math.Round(float64(b.Width) * c.scale))
	h := int(math.Round(float64(b.Height) * c.scale))

	clamped := false
	if x < 0 {
		w += x
		x = 0
		clamped = true
	}
	if y < 0 {
		h += y
		y = 0
		clamped = true
	}
	if x+w > c.opt.Canvas.Width {
		w = c.opt.Canvas.Width - x
		clamped = true
	}
	if y+h > c.opt.Canvas.Height {
		h = c.opt.Canvas.Height - y
		clamped = true
	}
	var diag *Diagnostic
	if clamped {
		diag = &Diagnostic{
			Code: DiagBoxClamped, Page: page, ElementID: elementID,
			Message: fmt.Sprintf("包围盒越界，已钳制为 (%d,%d %dx%d)", x, y, w, h),
		}
	}
	if w <= 0 || h <= 0 {
		return Box{X: x, Y: y, W: w, H: h}, false, &Diagnostic{
			Code: DiagAnchorMissing, Page: page, ElementID: elementID,
			Message: "包围盒换算后无面积，元素级动画无法挂载",
		}
	}
	return Box{X: x, Y: y, W: w, H: h}, true, diag
}

// resolveTrigger 把三种触发形式解析为页内锚点毫秒。
// 返回的是锚点而非最终起点：clip.startMs 的相对偏移由 compileClip 叠加。
func resolveTrigger(trigger string, offsets map[string]int64, page int, clipID string) (int64, bool, *Diagnostic) {
	switch {
	case trigger == "" || trigger == "onEnter":
		return 0, true, nil
	case len(trigger) > len("withSegment:") && trigger[:len("withSegment:")] == "withSegment:":
		seg := trigger[len("withSegment:"):]
		if off, ok := offsets[seg]; ok {
			if off < 0 {
				return 0, true, nil
			}
			return off, true, nil
		}
		return 0, true, &Diagnostic{
			Code: DiagSegmentUnresolved, Page: page, ClipID: clipID,
			Message: fmt.Sprintf("分段 %q 在当前讲稿修订中不存在，起点退化为 0", seg),
		}
	case len(trigger) > len("atTime:") && trigger[:len("atTime:")] == "atTime:":
		var ms int64
		if _, err := fmt.Sscanf(trigger[len("atTime:"):], "%d", &ms); err != nil {
			return 0, false, &Diagnostic{
				Code: "trigger_invalid", Page: page, ClipID: clipID,
				Message: fmt.Sprintf("无法解析触发 %q", trigger),
			}
		}
		if ms < 0 {
			return 0, true, nil
		}
		return ms, true, nil
	}
	return 0, false, &Diagnostic{
		Code: "trigger_invalid", Page: page, ClipID: clipID,
		Message: fmt.Sprintf("未知触发形式 %q", trigger),
	}
}

func frameMS(fps int) int64 {
	if fps <= 0 {
		fps = 30
	}
	return int64(math.Ceil(1000.0 / float64(fps)))
}
