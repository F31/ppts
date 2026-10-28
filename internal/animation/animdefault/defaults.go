package animdefault

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/F31/ppts/internal/animation"
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
}

// pageElement 是扁平化后的页内元素（用于默认生成）。
type pageElement struct {
	id   string
	kind string
	box  project.Box
}

// DefaultSpec 从页与预设生成一份可直接落库的默认规格。
//
// 确定性要求（设计方案 §14 M1 验收）：相同输入必须产出相同规格（id 也确定），
// 因为快照测试与"首次打开就能出片"都依赖它。时间戳与租户字段由接入层补齐。
func DefaultSpec(page *project.Page, opt DefaultOptions) (*animation.Revision, error) {
	if page == nil {
		return nil, errors.New("animation: page is nil")
	}
	if opt.DurationMS <= 0 {
		return nil, errors.New("animation: page duration is required")
	}
	presetID := opt.PresetID
	if presetID == "" {
		presetID = DefaultPresetID
	}
	preset, ok := animation.Catalog().Preset(presetID)
	if !ok {
		return nil, fmt.Errorf("animation: unknown preset %q", presetID)
	}

	elems := pageElements(page)
	pc := animation.PageContext{
		DurationMS: opt.DurationMS,
		FPS:        opt.FPS,
		Elements:   anchorsOf(elems),
		SegmentIDs: opt.SegmentIDs,
	}

	tracks := buildTracks(preset, elems, opt)
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
		return nil, fmt.Errorf("animation: default spec for preset %q is invalid: %w", presetID, err)
	}
	return rev, nil
}

func buildTracks(preset *animation.Preset, elems []pageElement, opt DefaultOptions) []*animation.Track {
	var tracks []*animation.Track
	clipSeq := 0
	bulletSeq := 0
	staggerSeq := 0
	segments := opt.SegmentIDs

	for _, el := range elems {
		if len(tracks) >= animation.MaxTracksPerSlide || clipSeq >= animation.MaxClipsPerSlide {
			break
		}
		templates := preset.Clips[el.kind]
		if len(templates) == 0 {
			continue
		}
		track := &animation.Track{ElementID: el.id, Kind: el.kind}
		start := int64(staggerSeq) * preset.StaggerMs
		trigger := animation.TriggerEnter

		if el.kind == animation.KindBullet && preset.SegmentAligned && bulletSeq < len(segments) {
			// 讲到哪条出现哪条：用相对锚定，配音重生成后无需改规格（§5.3）。
			trigger = animation.TriggerSegmentPrefix + segments[bulletSeq]
			start = 0
		}
		if el.kind == animation.KindBullet {
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
				out = append(out, pageElement{id: sh.ID, kind: kind, box: *sh.Bounds})
			}
			if len(sh.Children) > 0 {
				walk(sh.Children)
			}
		}
	}
	walk(page.Shapes)

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].box.Y != out[j].box.Y {
			return out[i].box.Y < out[j].box.Y
		}
		if out[i].box.X != out[j].box.X {
			return out[i].box.X < out[j].box.X
		}
		return out[i].id < out[j].id
	})

	// 阅读顺序里第一个有文字的元素当标题，其余文字元素当要点。
	// 没有字体信息可用，"第一个"是最稳的启发式：标题在版式上几乎总在最上。
	titleAssigned := false
	for i := range out {
		if out[i].kind != animation.KindShape {
			continue
		}
		if !titleAssigned {
			out[i].kind = animation.KindTitle
			titleAssigned = true
			continue
		}
		out[i].kind = animation.KindBullet
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
		out = append(out, animation.Anchor{ID: e.id, Kind: e.kind, HasBounds: true})
	}
	return out
}
