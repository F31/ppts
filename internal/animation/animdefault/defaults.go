package animdefault

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/animation/bind"
	"github.com/F31/ppts/internal/project"
)

// DefaultPresetID 是未指定预设时使用的默认套件（设计方案 §6.5）。
const DefaultPresetID = "business-calm"

// DefaultOptions 是默认生成所需的上下文。与 animation.PageContext 一样，只接受已换算好的
// 逻辑量，不引用时间轴类型。
type DefaultOptions struct {
	SlideID    string
	Language   string
	DurationMS int64
	FPS        int
	SegmentIDs []string
	PresetID   string // 空 = DefaultPresetID

	// Bindings 是「讲稿绑定预填」的结果（`animation/bind.Run` 的产出）。
	//
	// nil 与空切片**不是一回事**，这是本字段唯一的坑：
	//   - nil：「这次没做预填」，走既有的按顺序分配（第 N 条要点 → 第 N 段），
	//     产出与引入本字段之前逐字节相同；
	//   - 非 nil（哪怕零条）：「这次做了预填」，绑定说了算——没被绑的元素**保持静态**，
	//     不再按顺序兜底。零条意味着预填一条都没绑上，此时给出一份空轨道是诚实的：
	//     用户看到的是「没绑上」，而不是一份看起来正常、实际全是猜的动效。
	//
	// 为什么不合并成「有绑定就覆盖、没绑定就兜底」：那样「预填一条都没绑上」与
	// 「没做预填」会得到同一份产物，前端无从区分，用户也无从知道该不该手改。
	Bindings []bind.Binding
}

// Report 是一次默认生成里「绑定预填」的落地统计。只在 Bindings 非 nil 时有意义。
//
// 存在的理由：预填最危险的失败不是绑错，而是**悄无声息地少绑**——页面照样出片，
// 用户以为动效都排好了。把「几个元素被绑上、几个因此保持静态」透出来，
// 前端才能把这件事说出口。
type Report struct {
	Bound  int // 产出了绑定轨道的元素数
	Static int // 有动效模板、但因未绑定而保持静态的元素数
}

// Element 是默认生成看到的页内元素：id、动效类别、EMU 包围盒。
//
// 原先只有未导出的 pageElement，默认生成因此**只能**从 *project.Page 调用。
// 保真样片（scripts/fidelity/plan）手里是一份 elements.json：几何、文本、类别都在，
// 但没有 project.Page——它要验的恰恰是本包产出的轨道（讲稿绑定预填的第 5 组验收）。
// 于是把「元素清单」提为入口，DefaultSpec 变成它的一个包装。
type Element struct {
	ID   string
	Kind string
	Box  project.Box
}

// pageElement 保留旧名，避免把本包十几处引用一起改掉。
type pageElement = Element

// DefaultSpec 从页与预设生成一份可直接落库的默认规格。
//
// 确定性要求（设计方案 §14 M1 验收）：相同输入必须产出相同规格（id 也确定），
// 因为快照测试与"首次打开就能出片"都依赖它。时间戳与租户字段由接入层补齐。
func DefaultSpec(page *project.Page, opt DefaultOptions) (*animation.Revision, error) {
	rev, _, err := DefaultSpecReport(page, opt)
	return rev, err
}

// DefaultSpecReport 同 DefaultSpec，额外返回绑定预填的落地统计（Report）。
// Bindings 为 nil 时 Report 是全零——没有预填就没什么可统计的。
func DefaultSpecReport(page *project.Page, opt DefaultOptions) (*animation.Revision, Report, error) {
	if page == nil {
		return nil, Report{}, errors.New("animation: page is nil")
	}
	return SpecFromElements(pageElements(page), opt)
}

// SpecFromElements 用一份已扁平化的元素清单生成默认规格。
//
// DefaultSpecReport 是它的「从 project.Page 取元素」包装。分离出来的原因只有一个：
// 讲稿绑定预填的验收要拿**同一份轨生成代码**去跑保真样片，而样片手里是 elements.json
// （几何、类别、文本都在），没有 project.Page。为验收再造一份「差不多的」生成逻辑，
// 验的就只是那份复制品。类别的语义与 pageElements 一致，由调用方负责（默认入口仍走
// 阅读顺序归类）。
func SpecFromElements(elems []Element, opt DefaultOptions) (*animation.Revision, Report, error) {
	if opt.DurationMS <= 0 {
		return nil, Report{}, errors.New("animation: page duration is required")
	}
	presetID := opt.PresetID
	if presetID == "" {
		presetID = DefaultPresetID
	}
	preset, ok := animation.Catalog().Preset(presetID)
	if !ok {
		return nil, Report{}, fmt.Errorf("animation: unknown preset %q", presetID)
	}

	pc := animation.PageContext{
		DurationMS: opt.DurationMS,
		FPS:        opt.FPS,
		Elements:   anchorsOf(elems),
		SegmentIDs: opt.SegmentIDs,
	}

	tracks, report, err := buildTracks(preset, elems, opt)
	if err != nil {
		return nil, Report{}, err
	}
	rev := &animation.Revision{
		SlideID:   opt.SlideID,
		Language:  opt.Language,
		Status:    animation.StatusDraft,
		PresetID:  presetID,
		Intensity: preset.Intensity,
		Tracks:    tracks,
	}
	// 默认规格必须自己先过一遍校验：如果这里不合法，说明 catalog/预设写错了，
	// 应该在校验里暴露，而不是等用户第一次保存时才报错。
	if err := rev.Validate(pc); err != nil {
		return nil, Report{}, fmt.Errorf("animation: default spec for preset %q is invalid: %w", presetID, err)
	}
	return rev, report, nil
}

// buildTracks 按「有没有绑定预填」分两条路。两者**不混用**：一旦有绑定，就不再按
// 「第 N 条要点 → 第 N 段」的顺序分配——那是没有绑定信息时的替代品，两套同时生效
// 会让同一个元素既被绑到 seg-3、又被顺序逻辑拉到 seg-1，谁赢取决于遍历顺序。
func buildTracks(preset *animation.Preset, elems []pageElement, opt DefaultOptions) ([]*animation.Track, Report, error) {
	if opt.Bindings != nil {
		return buildBoundTracks(preset, elems, opt)
	}
	return buildSequencedTracks(preset, elems, opt), Report{}, nil
}

// buildBoundTracks 把预填结果翻译成轨道。
//
// 三条硬约束（changes/animation-narration-binding tasks 3.x）：
//   - 只写 `withSegment:<id>`，绝不写 `atTime:`——绝对毫秒在讲稿改稿后就错了；
//   - 没被绑定的元素保持静态（不出轨道），不拿 onEnter 兜底假装绑上了；
//   - 分段 id 必须真的在当前分段清单里，否则直接报错。这里不降级：绑定是刚由同一份
//     分段清单算出来的，对不上说明调用方把两份输入拼错了，属于编程错误而不是数据漂移。
func buildBoundTracks(preset *animation.Preset, elems []pageElement, opt DefaultOptions) ([]*animation.Track, Report, error) {
	segSet := make(map[string]struct{}, len(opt.SegmentIDs))
	for _, id := range opt.SegmentIDs {
		segSet[id] = struct{}{}
	}
	// 按元素 id 索引绑定。重复绑定保留第一条：绑定器输出已按元素去重，重复只可能来自
	// 调用方拼接了多份结果，此时"先来先得"比"后来覆盖"更好预测。
	byElem := make(map[string]bind.Binding, len(opt.Bindings))
	for _, b := range opt.Bindings {
		if _, dup := byElem[b.ElementID]; dup {
			continue
		}
		byElem[b.ElementID] = b
	}

	var tracks []*animation.Track
	var report Report
	clipSeq := 0

	for _, el := range elems {
		templates := preset.Clips[el.Kind]
		if len(templates) == 0 {
			// 这套预设本来就不动这类元素：既不是"绑上了"，也不是"因为没绑所以静态"。
			// 记进 Static 会把预设的能力边界说成预填的失职。
			continue
		}
		if len(tracks) >= animation.MaxTracksPerSlide || clipSeq >= animation.MaxClipsPerSlide {
			break
		}
		b, bound := byElem[el.ID]
		if !bound {
			report.Static++
			continue
		}
		if _, ok := segSet[b.SegmentID]; !ok {
			return nil, Report{}, fmt.Errorf(
				"animation: binding for element %q points at segment %q, which is not in this page's segment list",
				el.ID, b.SegmentID)
		}

		track := &animation.Track{ElementID: el.ID, Kind: el.Kind}
		for _, tpl := range templates {
			if clipSeq >= animation.MaxClipsPerSlide {
				break
			}
			duration := tpl.DurationMs
			if duration > opt.DurationMS {
				continue
			}
			start := b.StaggerMS
			if start+duration > opt.DurationMS {
				start = opt.DurationMS - duration
			}
			clipSeq++
			track.Clips = append(track.Clips, &animation.Clip{
				ID:         fmt.Sprintf("c%d", clipSeq),
				Type:       tpl.Type,
				StartMs:    start,
				DurationMs: duration,
				Easing:     tpl.Easing,
				Params:     tpl.Params,
				Trigger:    animation.TriggerSegmentPrefix + b.SegmentID,
				// 来源标注让编辑器能"先看同块带出的那 9 条"（tasks 4.1）；
				// 置信度让它们能按可疑程度升序排（tasks 4.2）。
				TriggerSource: b.Source,
				Confidence:    b.Score,
			})
		}
		if len(track.Clips) > 0 {
			tracks = append(tracks, track)
			report.Bound++
		} else {
			// 模板时长放不下（页太短）：这个元素最终没有轨道，如实记成静态。
			report.Static++
		}
	}

	if len(tracks) == 0 {
		// 预填一条都没绑上：**不**退化到整页淡入。整页淡入会让用户以为预填生效了，
		// 而真相是这一页什么都没对上。空轨道 + Report.Static 才是看得见的失败。
		return nil, report, nil
	}
	return tracks, report, nil
}

// buildSequencedTracks 是没有绑定信息时的既有行为：按阅读顺序错落出场，
// 要点按「第 N 条 → 第 N 段」相对锚定。逐字节保持原样（有 golden 钉住）。
func buildSequencedTracks(preset *animation.Preset, elems []pageElement, opt DefaultOptions) []*animation.Track {
	var tracks []*animation.Track
	clipSeq := 0
	bulletSeq := 0
	staggerSeq := 0
	segments := opt.SegmentIDs

	for _, el := range elems {
		if len(tracks) >= animation.MaxTracksPerSlide || clipSeq >= animation.MaxClipsPerSlide {
			break
		}
		templates := preset.Clips[el.Kind]
		if len(templates) == 0 {
			continue
		}
		track := &animation.Track{ElementID: el.ID, Kind: el.Kind}
		start := int64(staggerSeq) * preset.StaggerMs
		trigger := animation.TriggerEnter

		if el.Kind == animation.KindBullet && preset.SegmentAligned && bulletSeq < len(segments) {
			// 讲到哪条出现哪条：用相对锚定，配音重生成后无需改规格（§5.3）。
			trigger = animation.TriggerSegmentPrefix + segments[bulletSeq]
			start = 0
		}
		if el.Kind == animation.KindBullet {
			bulletSeq++
		}

		for _, tpl := range templates {
			if clipSeq >= animation.MaxClipsPerSlide {
				break
			}
			duration := tpl.DurationMs
			clipStart := start
			if duration > opt.DurationMS {
				// 页太短装不下这条动效：整条略过，而不是压到 0 时长（后者会产出一条
				// 视觉上等于"没有"却仍占预算的 clip）。
				continue
			}
			if clipStart+duration > opt.DurationMS {
				clipStart = opt.DurationMS - duration
			}
			clipSeq++
			track.Clips = append(track.Clips, &animation.Clip{
				ID:         fmt.Sprintf("c%d", clipSeq),
				Type:       tpl.Type,
				StartMs:    clipStart,
				DurationMs: duration,
				Easing:     tpl.Easing,
				Params:     tpl.Params,
				Trigger:    trigger,
			})
		}
		if len(track.Clips) > 0 {
			tracks = append(tracks, track)
			staggerSeq++
		}
	}

	if len(tracks) == 0 {
		// 无可挂载元素（或页太短）：退化为整页轨道，保证"零配置也能出一版"。
		if tpls := preset.Clips[animation.KindPage]; len(tpls) > 0 {
			tpl := tpls[0]
			duration := tpl.DurationMs
			if duration > opt.DurationMS {
				duration = opt.DurationMS
			}
			if eff, ok := animation.Catalog().Effect(tpl.Type); ok && duration >= eff.DurationRange[0] {
				cs := &animation.Clip{
					ID:         "c1",
					Type:       tpl.Type,
					DurationMs: duration,
					Easing:     tpl.Easing,
					Params:     tpl.Params,
					Trigger:    animation.TriggerEnter,
				}
				tracks = append(tracks, &animation.Track{Kind: animation.KindPage, Clips: []*animation.Clip{cs}})
			}
		}
	}
	return tracks
}

// pageElements 扁平化页内元素，按阅读顺序（上→下、左→右、id）排序，保证确定性。
// 无包围盒、未知形状、音视频不出现在动画轨道里（无法做区域动画）。
func pageElements(page *project.Page) []pageElement {
	var out []pageElement
	var walk func(shapes []*project.Shape)
	walk = func(shapes []*project.Shape) {
		for _, sh := range shapes {
			if sh == nil || sh.Bounds == nil || sh.Opaque || sh.ID == "" {
				continue
			}
			if kind, ok := classifyShape(sh); ok {
				out = append(out, Element{ID: sh.ID, Kind: kind, Box: *sh.Bounds})
			}
			if len(sh.Children) > 0 {
				walk(sh.Children)
			}
		}
	}
	walk(page.Shapes)

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Box.Y != out[j].Box.Y {
			return out[i].Box.Y < out[j].Box.Y
		}
		if out[i].Box.X != out[j].Box.X {
			return out[i].Box.X < out[j].Box.X
		}
		return out[i].ID < out[j].ID
	})

	// 阅读顺序里第一个有文字的元素当标题，其余文字元素当要点。
	// 没有字体信息可用，"第一个"是最稳的启发式：标题在版式上几乎总在最上。
	titleAssigned := false
	for i := range out {
		if out[i].Kind != animation.KindShape {
			continue
		}
		if !titleAssigned {
			out[i].Kind = animation.KindTitle
			titleAssigned = true
			continue
		}
		out[i].Kind = animation.KindBullet
	}
	return out
}

func classifyShape(sh *project.Shape) (string, bool) {
	kind := strings.ToLower(strings.TrimSpace(sh.Kind))
	switch kind {
	case "picture", "image":
		return animation.KindImage, true
	case "chart":
		return animation.KindChart, true
	case "table":
		return animation.KindTable, true
	case "audio", "video":
		return "", false
	}
	if sh.ChartType != "" {
		return animation.KindChart, true
	}
	if sh.TableRows > 0 || sh.TableCols > 0 {
		return animation.KindTable, true
	}
	if strings.TrimSpace(sh.Text) != "" {
		// 先按"形状"归类，标题/要点的区分在阅读顺序里统一决定。
		return animation.KindShape, true
	}
	if strings.Contains(kind, "icon") {
		return animation.KindIcon, true
	}
	return "", false
}

// PageAnchors 把页内形状扁平化为"可挂载动画"的锚点清单，供校验上下文与编辑器使用。
//
// 与 pageElements 的区别：这里不做标题/要点的语义归类，也不排除无文字的自选图形——
// 校验的 V2 只关心"这个元素在不在"，把可动画范围交给 catalog 的 appliesTo 判断。
// 若这里也按"能生成动效"筛一遍，用户给一个没文字的色块挂 pop 就会被误判为元素不存在。
func PageAnchors(page *project.Page) []animation.Anchor {
	if page == nil {
		return nil
	}
	var out []animation.Anchor
	var walk func(shapes []*project.Shape)
	walk = func(shapes []*project.Shape) {
		for _, sh := range shapes {
			if sh == nil || sh.ID == "" || sh.Opaque || sh.Bounds == nil {
				continue
			}
			kind := anchorKind(sh)
			if kind != "" {
				out = append(out, animation.Anchor{ID: sh.ID, Kind: kind, HasBounds: true})
			}
			if len(sh.Children) > 0 {
				walk(sh.Children)
			}
		}
	}
	walk(page.Shapes)
	return out
}

// anchorKind 只做"是否可挂载"的粗分：音视频不参与元素级动画，其余一律给出可用的 kind。
func anchorKind(sh *project.Shape) string {
	switch strings.ToLower(strings.TrimSpace(sh.Kind)) {
	case "audio", "video":
		return ""
	case "picture", "image":
		return animation.KindImage
	case "chart":
		return animation.KindChart
	case "table":
		return animation.KindTable
	}
	if sh.ChartType != "" {
		return animation.KindChart
	}
	if sh.TableRows > 0 || sh.TableCols > 0 {
		return animation.KindTable
	}
	return animation.KindShape
}

func anchorsOf(elems []pageElement) []animation.Anchor {
	out := make([]animation.Anchor, 0, len(elems))
	for _, e := range elems {
		out = append(out, animation.Anchor{ID: e.ID, Kind: e.Kind, HasBounds: true})
	}
	return out
}

// Anchors 返回**与默认规格同一套分类**的锚点清单：按阅读顺序排序，第一个文字元素算标题、
// 其余算要点。
//
// 为什么不能直接给编辑器用 PageAnchors：那份清单不做标题/要点归类（一律 shape），
// 于是面板里全是"形状 1 / 形状 2"，用户没法把某一行字和某一条轨道对上。
// 两份分类各写一遍更糟——预设生成的是"第 2 条要点"，编辑器却按另一套顺序编号，
// 用户改了"第 2 条"，动的却是第 3 条。
//
// 校验仍以 PageAnchors 为准（它是超集）：编辑器能选中的元素一定在校验范围内。
func Anchors(page *project.Page) []animation.Anchor {
	if page == nil {
		return nil
	}
	return anchorsOf(pageElements(page))
}
