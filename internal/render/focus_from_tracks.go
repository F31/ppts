package render

import (
	"fmt"
	"sort"
	"strings"

	"github.com/F31/ppts/internal/animation"
)

// FocusFromSegmentTracks 把 withSegment 轨道收成页级焦点：讲到哪条，哪条亮，其余绑过分段的要点变暗。
//
// 讲稿没有 Focus 字段时（配音生成的时间轴几乎都是这样），这是「教学清晰」能压暗的唯一来源。
// 少于两条可指的层就不必生成：只剩一个对象时压暗没有其余要点。
//
// layerIDs 必须是本页真正成层的 elementId。焦点校验要求 elementId 是图层，否则整页 IR 作废。
func FocusFromSegmentTracks(rev *animation.Revision, offsets map[string]int64, durationMs int64, layerIDs map[string]struct{}) []FocusSpan {
	if rev == nil || durationMs <= 0 || len(layerIDs) == 0 {
		return nil
	}
	type cand struct {
		id    string
		start int64
	}
	var cands []cand
	seenID := make(map[string]struct{})
	for _, track := range rev.Tracks {
		if track == nil || track.ElementID == "" {
			continue
		}
		if _, ok := layerIDs[track.ElementID]; !ok {
			continue
		}
		if _, dup := seenID[track.ElementID]; dup {
			continue
		}
		start, ok := firstSegmentStart(track, offsets, durationMs)
		if !ok {
			continue
		}
		seenID[track.ElementID] = struct{}{}
		cands = append(cands, cand{id: track.ElementID, start: start})
	}
	if len(cands) < 2 {
		return nil
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].start != cands[j].start {
			return cands[i].start < cands[j].start
		}
		return cands[i].id < cands[j].id
	})
	// 起点必须严格递增。同一毫秒两条会让求值器无法表达「此刻只亮一个」。
	out := make([]FocusSpan, 0, len(cands))
	var prev int64 = -1
	for _, c := range cands {
		if c.start <= prev {
			continue
		}
		out = append(out, FocusSpan{StartMs: c.start, ElementID: c.id})
		prev = c.start
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

func firstSegmentStart(track *animation.Track, offsets map[string]int64, durationMs int64) (int64, bool) {
	for _, clip := range track.Clips {
		if clip == nil {
			continue
		}
		if !strings.HasPrefix(clip.Trigger, animation.TriggerSegmentPrefix) {
			continue
		}
		seg := strings.TrimPrefix(clip.Trigger, animation.TriggerSegmentPrefix)
		if seg == "" {
			continue
		}
		off, ok := offsets[seg]
		if !ok {
			continue
		}
		start := off + clip.StartMs
		if start < 0 {
			start = 0
		}
		if start >= durationMs {
			continue
		}
		return start, true
	}
	return 0, false
}

func presetFocusFollowsSegment(rev *animation.Revision) bool {
	if rev == nil || rev.PresetID == "" {
		return false
	}
	p, ok := animation.Catalog().Preset(rev.PresetID)
	return ok && p.FocusFollowsSegment
}

const gazeMinAreaRatio = 0.002

func bakedVeilAlpha(intensity float64) float64 {
	if intensity <= 0 {
		intensity = 1
	}
	a := 0.45 * intensity
	if a < 0.28 {
		a = 0.28
	}
	if a > 0.60 {
		a = 0.60
	}
	return a
}

// GazeFromSegmentTracks 把已铺开的 spotlight/highlight 轨道收成带 Rect 的注视候选。
//
// 读 clip.StartMs + 分段偏移。返回未合并列表；CompilePage 是唯一的 mergeGazeLooks 调用点。
// denyIDs 为页码/图注等，nil 表示不额外过滤。
func GazeFromSegmentTracks(rev *animation.Revision, offsets map[string]int64, durationMs int64, elements []Element, scale float64, canvasW, canvasH int, denyIDs map[string]struct{}) []FocusSpan {
	if rev == nil || durationMs <= 0 || canvasW <= 0 || canvasH <= 0 {
		return nil
	}
	byID := make(map[string]Element, len(elements))
	for _, el := range elements {
		byID[el.ID] = el
	}
	targetIDs := make(map[string]struct{}, len(rev.Tracks))
	for _, track := range rev.Tracks {
		if track != nil && track.ElementID != "" {
			targetIDs[track.ElementID] = struct{}{}
		}
	}
	pageArea := float64(canvasW) * float64(canvasH)
	frames := gazeFrameBoxes(elements, scale, canvasW, canvasH, pageArea)
	targets := gazeTargetBoxes(elements, scale, canvasW, canvasH, targetIDs)
	type cand struct {
		id    string
		start int64
		rect  Box
		group string
	}
	var cands []cand
	seenID := make(map[string]struct{})
	for _, track := range rev.Tracks {
		if track == nil || track.ElementID == "" {
			continue
		}
		if _, dup := seenID[track.ElementID]; dup {
			continue
		}
		if _, deny := denyIDs[track.ElementID]; deny {
			continue
		}
		el, ok := byID[track.ElementID]
		if !ok {
			continue
		}
		if skipGazeFocusKind(el.Kind) {
			continue
		}
		raw, _, rawOK, _ := boxOf(el.Bounds, scale, canvasW, canvasH)
		if rawOK && isPageHeading(el.Kind, raw, canvasW, canvasH) {
			continue
		}
		rect, group, ok := gazeRectForTarget(el, frames, blockedTargetBoxes(targets, el.ID), scale, canvasW, canvasH, pageArea)
		if !ok {
			continue
		}
		start, ok := firstGazeSegmentStart(track, offsets, durationMs)
		if !ok {
			continue
		}
		seenID[track.ElementID] = struct{}{}
		cands = append(cands, cand{id: track.ElementID, start: start, rect: rect, group: group})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].start != cands[j].start {
			return cands[i].start < cands[j].start
		}
		return cands[i].id < cands[j].id
	})
	veil := bakedVeilAlpha(rev.Intensity)
	out := make([]FocusSpan, 0, len(cands))
	var prev int64 = -1
	for _, c := range cands {
		if c.start <= prev {
			continue
		}
		rect := c.rect
		out = append(out, FocusSpan{
			StartMs: c.start, ElementID: c.id, Rect: &rect,
			FadeMs: focusFadeDefaultMS, VeilAlpha: veil, group: c.group,
		})
		prev = c.start
	}
	return out
}

func gazeFrameBoxes(elements []Element, scale float64, canvasW, canvasH int, pageArea float64) []Box {
	out := make([]Box, 0, len(elements))
	for _, el := range elements {
		if el.Kind != animation.KindShape {
			continue
		}
		box, _, ok, _ := boxOf(el.Bounds, scale, canvasW, canvasH)
		if !ok {
			continue
		}
		if pageArea > 0 && boxArea(box)/pageArea >= gazeMaxFrameRatio {
			continue
		}
		out = append(out, box)
	}
	return out
}

// gazeRectForTarget 在扩框时避开覆盖其他注视目标的共享面板，并返回本次 look 的组身份。
// 组身份只用于编译期合并：同卡标题/正文共享一个 frame，目录并列条目各自一个 el。
func gazeRectForTarget(el Element, frames, blocked []Box, scale float64, canvasW, canvasH int, pageArea float64) (Box, string, bool) {
	box, _, ok, _ := boxOf(el.Bounds, scale, canvasW, canvasH)
	if !ok {
		return Box{}, "", false
	}
	area := boxArea(box)
	if pageArea > 0 && (area/pageArea <= gazeMinAreaRatio || area/pageArea >= plateFullPageRatio) {
		return Box{}, "", false
	}
	expanded := expandGazeRectForTarget(box, frames, blocked, pageArea)
	group := "el:" + el.ID
	if expanded != box {
		group = fmt.Sprintf("frame:%d:%d:%d:%d", expanded.X, expanded.Y, expanded.W, expanded.H)
	}
	return PadGazeRect(expanded, canvasW, canvasH), group, true
}

func gazeTargetBoxes(elements []Element, scale float64, canvasW, canvasH int, ids map[string]struct{}) map[string]Box {
	out := make(map[string]Box, len(ids))
	for _, el := range elements {
		if el.ID == "" || el.Bounds == nil {
			continue
		}
		if ids != nil {
			if _, ok := ids[el.ID]; !ok {
				continue
			}
		}
		box, _, ok, _ := boxOf(el.Bounds, scale, canvasW, canvasH)
		if !ok {
			continue
		}
		out[el.ID] = box
	}
	return out
}

func blockedTargetBoxes(targets map[string]Box, self string) []Box {
	if len(targets) <= 1 {
		return nil
	}
	out := make([]Box, 0, len(targets)-1)
	for id, box := range targets {
		if id == self {
			continue
		}
		out = append(out, box)
	}
	return out
}

// mergeGazeLooks 按组 + 矩形去重：同组（同一张卡）保留 StartMs 更晚的正文，不同组不合并。
// 更晚的起点让 gaze+entrance 的 look 仍帧采在正文入场之后。keepTimes 时按时间序排并重算 EndMs，
// 禁止 (Y,X) 与 duration/n。回退路径保持今日阅读序重铺。
func mergeGazeLooks(spans []FocusSpan, durationMs int64, keepTimes bool) []FocusSpan {
	if len(spans) < 2 {
		return spans
	}
	uniq := make([]FocusSpan, 0, len(spans))
	for _, s := range spans {
		if s.Rect == nil {
			uniq = append(uniq, s)
			continue
		}
		dupAt := -1
		for i, u := range uniq {
			if u.Rect == nil || !rectNear(*u.Rect, *s.Rect) {
				continue
			}
			// 有组身份时，只有同组才允许矩形去重。目录三行共用面板但组不同，
			// 不能被「矩形相等」静默吞掉。
			if s.group != "" || u.group != "" {
				if s.group != u.group {
					continue
				}
			}
			dupAt = i
			break
		}
		if dupAt < 0 {
			uniq = append(uniq, s)
			continue
		}
		sameEl := s.ElementID != "" && s.ElementID == uniq[dupAt].ElementID
		if keepTimes && sameEl && s.StartMs-uniq[dupAt].StartMs >= lookPhraseGapMS {
			uniq = append(uniq, s)
			continue
		}
		if s.StartMs > uniq[dupAt].StartMs {
			uniq[dupAt] = s
		}
	}
	if len(uniq) == len(spans) && !keepTimes {
		return spans
	}
	if keepTimes {
		sort.SliceStable(uniq, func(i, j int) bool {
			if uniq[i].StartMs != uniq[j].StartMs {
				return uniq[i].StartMs < uniq[j].StartMs
			}
			return uniq[i].ElementID < uniq[j].ElementID
		})
		for i := range uniq {
			if i+1 < len(uniq) {
				uniq[i].EndMs = uniq[i+1].StartMs
			} else {
				uniq[i].EndMs = 0
			}
		}
		return uniq
	}
	sort.SliceStable(uniq, func(i, j int) bool {
		ri, rj := uniq[i].Rect, uniq[j].Rect
		if ri == nil && rj == nil {
			return uniq[i].StartMs < uniq[j].StartMs
		}
		if ri == nil {
			return false
		}
		if rj == nil {
			return true
		}
		if ri.Y != rj.Y {
			return ri.Y < rj.Y
		}
		if ri.X != rj.X {
			return ri.X < rj.X
		}
		return uniq[i].StartMs < uniq[j].StartMs
	})
	n := int64(len(uniq))
	if n > 0 && durationMs > 0 {
		step := durationMs / n
		for i := range uniq {
			uniq[i].StartMs = int64(i) * step
			uniq[i].EndMs = 0
		}
	}
	return uniq
}

func firstGazeSegmentStart(track *animation.Track, offsets map[string]int64, durationMs int64) (int64, bool) {
	for _, clip := range track.Clips {
		if clip == nil || !animation.IsGazeMarker(clip.Type) {
			continue
		}
		var start int64
		switch {
		case strings.HasPrefix(clip.Trigger, animation.TriggerSegmentPrefix):
			seg := strings.TrimPrefix(clip.Trigger, animation.TriggerSegmentPrefix)
			if seg == "" {
				continue
			}
			off, ok := offsets[seg]
			if !ok {
				continue
			}
			start = off + clip.StartMs
		case clip.Trigger == "" || clip.Trigger == animation.TriggerEnter:
			start = clip.StartMs
		default:
			continue
		}
		if start < 0 {
			start = 0
		}
		if start >= durationMs {
			continue
		}
		return start, true
	}
	return 0, false
}

// compileGazeFocus 在 compileTrack 之后填 Focus。拍表几何失败时另铺 orig 只给 Focus，
// 不改 in.Revision、不重跑 compileTrack。
func (c *Compiler) compileGazeFocus(in PageInput, orig *animation.Revision) []FocusSpan {
	cw, ch := c.opt.Canvas.Width, c.opt.Canvas.Height
	var focus []FocusSpan
	if in.BeatSheet.Valid() {
		focus = dropContainedLooks(mergeGazeLooks(c.focusFromBeatSheet(in, orig, false), in.DurationMs, true))
	}
	if len(focus) < 2 {
		filled := fillFocusRects(in.Focus, in.Elements, c.scale, cw, ch)
		if gazeFocusReady(filled) {
			focus = dropContainedLooks(mergeGazeLooks(bakeGazeFocus(filled, orig), in.DurationMs, false))
		}
	}
	if len(focus) < 2 {
		fallback := revisionWithSpreadStarts(orig, in.SegmentOffsets, in.DurationMs, false)
		cands := GazeFromSegmentTracks(fallback, in.SegmentOffsets, in.DurationMs, in.Elements, c.scale, cw, ch, in.GazeDenyIDs)
		focus = dropContainedLooks(mergeGazeLooks(cands, in.DurationMs, false))
	}
	// 表格页旁白点的是数据行：行不当圈选之后往往不足 2 条，整页掉出 look 链走
	// Chrome。没有其它注视时才让行成为圈选，混合页（卡片+表）仍只淡入行。
	if len(focus) < 2 && in.BeatSheet.Valid() {
		focus = dropContainedLooks(mergeGazeLooks(c.focusFromBeatSheet(in, orig, true), in.DurationMs, true))
	}
	if len(focus) < 2 {
		return nil
	}
	return focus
}

func (c *Compiler) focusFromBeatSheet(in PageInput, orig *animation.Revision, allowRow bool) []FocusSpan {
	if in.BeatSheet == nil {
		return nil
	}
	cw, ch := c.opt.Canvas.Width, c.opt.Canvas.Height
	pageArea := float64(cw) * float64(ch)
	frames := gazeFrameBoxes(in.Elements, c.scale, cw, ch, pageArea)
	byID := make(map[string]Element, len(in.Elements))
	for _, el := range in.Elements {
		byID[el.ID] = el
	}
	beats := append([]animation.Beat(nil), in.BeatSheet.Beats...)
	sort.SliceStable(beats, func(i, j int) bool {
		if beats[i].StartMs != beats[j].StartMs {
			return beats[i].StartMs < beats[j].StartMs
		}
		return beats[i].ShapeID < beats[j].ShapeID
	})
	intensity := 1.0
	if orig != nil && orig.Intensity > 0 {
		intensity = orig.Intensity
	}
	veil := bakedVeilAlpha(intensity)
	targetIDs := make(map[string]struct{}, len(beats))
	for _, b := range beats {
		if b.ShapeID == "" || b.Shot == animation.BeatShotHold {
			continue
		}
		if _, deny := in.GazeDenyIDs[b.ShapeID]; deny {
			continue
		}
		el, ok := byID[b.ShapeID]
		if !ok || skipGazeActorKind(el.Kind, allowRow) {
			continue
		}
		raw, _, rawOK, _ := boxOf(el.Bounds, c.scale, cw, ch)
		if rawOK && isPageHeading(el.Kind, raw, cw, ch) {
			continue
		}
		targetIDs[b.ShapeID] = struct{}{}
	}
	targets := gazeTargetBoxes(in.Elements, c.scale, cw, ch, targetIDs)
	out := make([]FocusSpan, 0, len(beats))
	var prev int64 = -1
	var lastID string
	var lastRect Box
	var lastGroup string
	phraseExtra := 0
	for _, b := range beats {
		id := b.ShapeID
		hold := id == "" || b.Shot == animation.BeatShotHold
		if hold {
			if lastID == "" || phraseExtra >= maxPhraseLooks {
				continue
			}
			dur := b.EndMs - b.StartMs
			if b.EndMs <= b.StartMs || dur < minPhraseHoldMS {
				continue
			}
			if b.StartMs <= prev {
				continue
			}
			r := lastRect
			out = append(out, FocusSpan{
				StartMs:   b.StartMs,
				ElementID: lastID,
				Rect:      &r,
				FadeMs:    focusFadeDefaultMS,
				VeilAlpha: veil,
				group:     lastGroup,
			})
			prev = b.StartMs
			phraseExtra++
			continue
		}
		if _, deny := in.GazeDenyIDs[id]; deny {
			continue
		}
		el, ok := byID[id]
		if !ok {
			continue
		}
		if skipGazeActorKind(el.Kind, allowRow) {
			continue
		}
		raw, _, rawOK, _ := boxOf(el.Bounds, c.scale, cw, ch)
		if rawOK && isPageHeading(el.Kind, raw, cw, ch) {
			continue
		}
		rect, group, ok := gazeRectForTarget(el, frames, blockedTargetBoxes(targets, id), c.scale, cw, ch, pageArea)
		if !ok {
			continue
		}
		if b.StartMs <= prev {
			continue
		}
		r := rect
		out = append(out, FocusSpan{
			StartMs:   b.StartMs,
			ElementID: id,
			Rect:      &r,
			FadeMs:    focusFadeDefaultMS,
			VeilAlpha: veil,
			group:     group,
		})
		prev = b.StartMs
		lastID = id
		lastRect = rect
		lastGroup = group
	}
	for i := range out {
		if i+1 < len(out) {
			out[i].EndMs = out[i+1].StartMs
		}
	}
	out = preferUniqueFocus(out)
	if len(out) < 2 {
		return nil
	}
	return out
}

func uniqueFocusIDs(spans []FocusSpan) int {
	seen := make(map[string]struct{}, len(spans))
	for _, s := range spans {
		if s.ElementID != "" {
			seen[s.ElementID] = struct{}{}
		}
	}
	return len(seen)
}

func firstLookPerElement(spans []FocusSpan) []FocusSpan {
	seen := make(map[string]struct{}, len(spans))
	out := make([]FocusSpan, 0, len(spans))
	for _, s := range spans {
		if s.ElementID == "" {
			continue
		}
		if _, ok := seen[s.ElementID]; ok {
			continue
		}
		seen[s.ElementID] = struct{}{}
		out = append(out, s)
	}
	for i := range out {
		if i+1 < len(out) {
			out[i].EndMs = out[i+1].StartMs
		}
	}
	return out
}

// preferUniqueFocus 密页丢掉同一元素的词级加拍，互异形状全部保留。
func preferUniqueFocus(spans []FocusSpan) []FocusSpan {
	if uniqueFocusIDs(spans) >= denseGazeLooks {
		return firstLookPerElement(spans)
	}
	return spans
}

func fillFocusRects(spans []FocusSpan, elements []Element, scale float64, canvasW, canvasH int) []FocusSpan {
	if len(spans) == 0 {
		return nil
	}
	byID := make(map[string]Element, len(elements))
	for _, el := range elements {
		byID[el.ID] = el
	}
	pageArea := float64(canvasW) * float64(canvasH)
	frames := gazeFrameBoxes(elements, scale, canvasW, canvasH, pageArea)
	targetIDs := make(map[string]struct{}, len(spans))
	for _, s := range spans {
		if s.ElementID != "" {
			targetIDs[s.ElementID] = struct{}{}
		}
	}
	targets := gazeTargetBoxes(elements, scale, canvasW, canvasH, targetIDs)
	out := make([]FocusSpan, 0, len(spans))
	for _, s := range spans {
		el, ok := byID[s.ElementID]
		if !ok {
			out = append(out, s)
			continue
		}
		if skipGazeFocusKind(el.Kind) {
			continue
		}
		raw, _, rawOK, _ := boxOf(el.Bounds, scale, canvasW, canvasH)
		if rawOK && isPageHeading(el.Kind, raw, canvasW, canvasH) {
			continue
		}
		rect, group, ok := gazeRectForTarget(el, frames, blockedTargetBoxes(targets, el.ID), scale, canvasW, canvasH, pageArea)
		if !ok {
			out = append(out, s)
			continue
		}
		s.Rect = &rect
		s.group = group
		out = append(out, s)
	}
	return out
}

func gazeFocusReady(spans []FocusSpan) bool {
	if len(spans) == 0 {
		return false
	}
	for _, s := range spans {
		if s.Rect == nil || s.Rect.W <= 0 || s.Rect.H <= 0 {
			return false
		}
	}
	return true
}

func dropFocusWithoutRect(spans []FocusSpan) []FocusSpan {
	out := spans[:0]
	for _, s := range spans {
		if s.Rect != nil && s.Rect.W > 0 && s.Rect.H > 0 {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func annotationsFromFocus(spans []FocusSpan, callout string) []Annotation {
	if len(spans) == 0 {
		return nil
	}
	switch callout {
	case "", "off":
		return nil
	}
	ready := make([]FocusSpan, 0, len(spans))
	for _, s := range spans {
		if s.Rect != nil && s.Rect.W > 0 && s.Rect.H > 0 {
			ready = append(ready, s)
		}
	}
	out := make([]Annotation, 0, len(ready))
	for i, s := range ready {
		shape := calloutShape(*s.Rect)
		switch callout {
		case "rect":
			shape = AnnotationShapeRect
		case "circle":
			shape = AnnotationShapeCircle
		}
		end := s.EndMs
		// Focus EndMs=0 表示「保持到下一段」。evaluateAnnotation 把 0 当成永不放手，
		// 圈选会在页上越叠越多。非末段必须写成下一段的起点，换段 240ms 交叉淡出。
		if end == 0 && i+1 < len(ready) && ready[i+1].StartMs > s.StartMs {
			end = ready[i+1].StartMs
		}
		out = append(out, Annotation{
			Kind:      AnnotationCallout,
			StartMs:   s.StartMs,
			EndMs:     end,
			FadeMs:    s.FadeMs,
			ElementID: s.ElementID,
			Rect:      s.Rect,
			Shape:     shape,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func calloutShape(b Box) string {
	_ = b
	return AnnotationShapeRect
}

// skipGazeFocusKind 是入场演员，默认不当注视圈选。柱子生长必须进 Chrome；
// 表格行先不当圈选，只在 compileGazeFocus 发现整页不足 2 条注视时才放行。
func skipGazeFocusKind(kind string) bool {
	return skipGazeActorKind(kind, false)
}

func skipGazeActorKind(kind string, allowRow bool) bool {
	switch kind {
	case animation.KindBar:
		return true
	case animation.KindRow:
		return !allowRow
	default:
		return false
	}
}

// isPageHeading 是页眉大标题：靠上、不太高。
//
// 阅读序第一个文字标成 title，但角标（「大会全景」）Y 更小会抢走 kind，真正的
// 大标题变成 bullet。title 用 20% 宽（角标也算页眉）；bullet/shape 必须过半宽，
// 才不会把靠上的要点条当成标题。六宫格卡片标题 Y 过不了 15%。
func isPageHeading(kind string, box Box, canvasW, canvasH int) bool {
	if canvasW <= 0 || canvasH <= 0 {
		return false
	}
	if box.Y >= int(float64(canvasH)*0.15) {
		return false
	}
	if box.H >= int(float64(canvasH)*0.18) {
		return false
	}
	minW := float64(canvasW) * 0.50
	switch kind {
	case animation.KindTitle:
		minW = float64(canvasW) * 0.20
	case animation.KindBullet, animation.KindShape:
	default:
		return false
	}
	return box.W > int(minW)
}

// headingClearPadPx 让标题笔画不贴着纱边被羽化吃掉。
const headingClearPadPx = 4
const maxHeadingClearRects = 4

// headingClearRects 收集页眉大标题的画布矩形，供合成器贴在纱之上。
func headingClearRects(elements []Element, scale float64, canvasW, canvasH int) []Box {
	if canvasW <= 0 || canvasH <= 0 {
		return nil
	}
	var out []Box
	for _, el := range elements {
		raw, _, ok, _ := boxOf(el.Bounds, scale, canvasW, canvasH)
		if !ok || !isPageHeading(el.Kind, raw, canvasW, canvasH) {
			continue
		}
		out = append(out, padBox(raw, headingClearPadPx, canvasW, canvasH))
		if len(out) >= maxHeadingClearRects {
			break
		}
	}
	return out
}

func padBox(b Box, pad, canvasW, canvasH int) Box {
	if pad < 0 {
		pad = 0
	}
	x := b.X - pad
	y := b.Y - pad
	w := b.W + pad*2
	h := b.H + pad*2
	if x < 0 {
		w += x
		x = 0
	}
	if y < 0 {
		h += y
		y = 0
	}
	if x+w > canvasW {
		w = canvasW - x
	}
	if y+h > canvasH {
		h = canvasH - y
	}
	if w < 1 || h < 1 {
		return b
	}
	return Box{X: x, Y: y, W: w, H: h}
}

// dropContainedLooks 丢掉落在更大 look 里面的小框（卡片标题 vs 卡片底）。
func dropContainedLooks(spans []FocusSpan) []FocusSpan {
	if len(spans) < 2 {
		return spans
	}
	drop := make([]bool, len(spans))
	for i := range spans {
		if spans[i].Rect == nil {
			continue
		}
		ai := boxArea(*spans[i].Rect)
		for j := range spans {
			if i == j || spans[j].Rect == nil {
				continue
			}
			if boxArea(*spans[j].Rect) <= ai {
				continue
			}
			if spans[i].group != "" || spans[j].group != "" {
				if spans[i].group != spans[j].group {
					continue
				}
			}
			if boxCovers(*spans[j].Rect, *spans[i].Rect) {
				drop[i] = true
				break
			}
		}
	}
	out := make([]FocusSpan, 0, len(spans))
	for i, s := range spans {
		if !drop[i] {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func bakeGazeFocus(spans []FocusSpan, rev *animation.Revision) []FocusSpan {
	if len(spans) == 0 {
		return nil
	}
	intensity := 1.0
	if rev != nil && rev.Intensity > 0 {
		intensity = rev.Intensity
	}
	veil := bakedVeilAlpha(intensity)
	for i := range spans {
		if spans[i].VeilAlpha == 0 {
			spans[i].VeilAlpha = veil
		}
		if spans[i].FadeMs == 0 {
			spans[i].FadeMs = focusFadeDefaultMS
		}
	}
	return spans
}
