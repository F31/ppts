package animation

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// 校验规则编号，与设计方案 §5.4 一一对应。V0 是本实现补充的结构完整性检查
// （缺失字段、非法缓动、非法 trigger 形式），设计方案未给编号，单独编号以免混淆。
const (
	RuleStructure   = "V0"
	RuleType        = "V1"
	RuleAnchor      = "V2"
	RuleTime        = "V3"
	RuleOverlap     = "V4"
	RuleIntensity   = "V5"
	RuleBudget      = "V6"
	RuleSegment     = "V7"
	RulePreset      = "V8"
	RuleLocked      = "V9"
	RuleConcurrency = "V10"
	RuleScale       = "V11"
	RuleBytes       = "V12"
)

const pageLevelTrackKey = "\x00page"

// Validate 执行 V1–V8、V11–V12（V9/V10 依赖库中状态，见 CheckWritable）。
//
// 判定顺序不是随意的：规模与预设 → 强度 → 逐元素/逐 clip → 跨 clip 的时间约束。
// 先做便宜且能一次性给出的判断，再做需要聚合的判断，用户一次就能看到最该先修的问题。
func (r *Revision) Validate(pc PageContext) error {
	if r == nil {
		return &ValidationError{Rule: RuleStructure, Message: "spec is nil", Status: 400}
	}

	// V11：规模上限（防失控）。先判，避免后面为一份明显失控的规格做全量遍历。
	if n := len(r.Tracks); n > MaxTracksPerSlide {
		return &ValidationError{
			Rule: RuleScale, Field: "tracks",
			Message: fmt.Sprintf("%d / %d tracks", n, MaxTracksPerSlide),
			Hint:    "合并同元素动效，或拆到多页",
			Status:  400,
		}
	}
	if n := r.ClipCount(); n > MaxClipsPerSlide {
		return &ValidationError{
			Rule: RuleScale, Field: "clips",
			Message: fmt.Sprintf("%d / %d clips", n, MaxClipsPerSlide),
			Hint:    "减少元素级动效数量",
			Status:  400,
		}
	}

	// V12：字节上限。防的是极端参数/元素 id 造成的存储与传输放大。
	if size := r.Size(); size > MaxSpecBytes {
		return &ValidationError{
			Rule: RuleBytes, Field: "spec",
			Message: fmt.Sprintf("%d / %d bytes", size, MaxSpecBytes),
			Status:  413,
		}
	}

	// V8：预设必须存在。空串表示自定义，允许。
	if r.PresetID != "" {
		if _, ok := Catalog().Preset(r.PresetID); !ok {
			return &ValidationError{
				Rule: RulePreset, Field: "presetId",
				Message: fmt.Sprintf("unknown preset %q", r.PresetID),
				Hint:    strings.Join(presetIDs(), ", "),
				Status:  400,
			}
		}
	}

	// V5（页级部分）：强度。
	if r.Intensity < MinIntensity || r.Intensity > MaxIntensity {
		return &ValidationError{
			Rule: RuleIntensity, Field: "intensity",
			Message: fmt.Sprintf("%v outside [%v, %v]", r.Intensity, MinIntensity, MaxIntensity),
			Status:  400,
		}
	}

	tolerance := pc.FrameMS()
	anchors := make(map[string]Anchor, len(pc.Elements))
	for _, a := range pc.Elements {
		anchors[a.ID] = a
	}
	segments := make(map[string]struct{}, len(pc.SegmentIDs))
	for _, id := range pc.SegmentIDs {
		segments[id] = struct{}{}
	}

	// V4 的聚合桶：按元素 × 类别收集，最后统一判重叠。
	byElement := map[string]map[string][]*Clip{}
	var elementOrder []string

	for ti, t := range r.Tracks {
		field := fmt.Sprintf("tracks[%d]", ti)
		if t == nil {
			return invalid(RuleStructure, field, "null track", 400)
		}
		if t.Kind == "" {
			return invalid(RuleStructure, field+".kind", "missing kind", 400)
		}
		if !validKind(t.Kind) {
			return &ValidationError{
				Rule: RuleType, Field: field + ".kind",
				Message: fmt.Sprintf("unknown kind %q", t.Kind),
				Hint:    "page|title|bullet|image|icon|chart|table|shape",
				Status:  400,
			}
		}
		if len(t.Clips) == 0 {
			return invalid(RuleScale, field+".clips", "track has no clips", 400)
		}

		// V2：锚点必须存在于当前页；空 elementId 只允许页面级轨道。
		if t.ElementID == "" {
			if t.Kind != KindPage {
				return &ValidationError{
					Rule: RuleAnchor, Field: field + ".elementId",
					Message: fmt.Sprintf("empty elementId requires kind %q, got %q", KindPage, t.Kind),
					Status:  400,
				}
			}
		} else if _, ok := anchors[t.ElementID]; !ok {
			return &ValidationError{
				Rule: RuleAnchor, Field: field + ".elementId",
				Message: fmt.Sprintf("element %q not found on slide", t.ElementID),
				Hint:    anchorHint(anchors),
				Status:  400,
			}
		}

		key := t.ElementID
		if key == "" {
			key = pageLevelTrackKey
			if _, seen := byElement[key]; !seen {
				elementOrder = append(elementOrder, key)
			}
		} else if _, seen := byElement[key]; !seen {
			elementOrder = append(elementOrder, key)
		}
		if byElement[key] == nil {
			byElement[key] = map[string][]*Clip{}
		}

		for ci, c := range t.Clips {
			cf := fmt.Sprintf("%s.clips[%d]", field, ci)
			if c == nil {
				return invalid(RuleStructure, cf, "null clip", 400)
			}
			if c.Type == "" {
				return invalid(RuleStructure, cf+".type", "missing type", 400)
			}
			eff, ok := Catalog().Effect(c.Type)
			if !ok {
				return &ValidationError{
					Rule: RuleType, Field: cf + ".type",
					Message: fmt.Sprintf("unknown effect %q", c.Type),
					Hint:    strings.Join(Catalog().EffectIDs(), ", "),
					Status:  400,
				}
			}
			if !eff.Applies(t.Kind) {
				return &ValidationError{
					Rule: RuleType, Field: cf + ".type",
					Message: fmt.Sprintf("effect %q does not apply to kind %q", c.Type, t.Kind),
					Hint:    strings.Join(eff.AppliesTo, ", "),
					Status:  400,
				}
			}
			if _, ok := validEasings[c.Easing]; !ok {
				return &ValidationError{
					Rule: RuleStructure, Field: cf + ".easing",
					Message: fmt.Sprintf("unknown easing %q", c.Easing),
					Hint:    "linear|easeOut|easeInOut|spring",
					Status:  400,
				}
			}
			if err := validateClipTiming(c, eff, pc, tolerance, cf); err != nil {
				return err
			}
			if err := validateClipTrigger(c, segments, pc, tolerance, cf); err != nil {
				return err
			}
			if err := validateClipParams(c, eff, cf); err != nil {
				return err
			}
			class := eff.Class
			byElement[key][class] = append(byElement[key][class], c)
		}
	}

	// V4：入场/退场各最多一条；强调可多条但不允许同类型时间重叠。
	for _, key := range elementOrder {
		groups := byElement[key]
		for _, class := range []string{ClassEntrance, ClassExit} {
			if clips := groups[class]; len(clips) > 1 {
				return &ValidationError{
					Rule: RuleOverlap, Field: clipField(r, key),
					Message: fmt.Sprintf("%d %s clips on one element", len(clips), class),
					Hint:    clipIDs(clips),
					Status:  400,
				}
			}
		}
		emphasis := groups[ClassEmphasis]
		for i := 0; i < len(emphasis); i++ {
			for j := i + 1; j < len(emphasis); j++ {
				if emphasis[i].Type != emphasis[j].Type {
					continue
				}
				if intervalsOverlap(emphasis[i], emphasis[j]) {
					return &ValidationError{
						Rule: RuleOverlap, Field: clipField(r, key),
						Message: fmt.Sprintf("%q clips overlap", emphasis[i].Type),
						Hint:    emphasis[i].ID + " / " + emphasis[j].ID,
						Status:  400,
					}
				}
			}
		}
	}

	// V6：整页时间预算（重叠归一后不得超过页时长）。
	if pc.DurationMS > 0 {
		var spans []span
		for _, t := range r.Tracks {
			for _, c := range t.Clips {
				if c == nil {
					continue
				}
				spans = append(spans, span{start: c.StartMs, end: c.StartMs + c.DurationMs})
			}
		}
		if used := unionLength(spans); used > pc.DurationMS+tolerance {
			return &ValidationError{
				Rule: RuleBudget, Field: "tracks",
				Message: fmt.Sprintf("animation covers %dms, page budget %dms", used, pc.DurationMS),
				Hint:    "缩短时长或减少动效",
				Status:  400,
			}
		}
	}

	return nil
}

func invalid(rule, field, msg string, status int) *ValidationError {
	return &ValidationError{Rule: rule, Field: field, Message: msg, Status: status}
}

func validateClipTiming(c *Clip, eff *Effect, pc PageContext, tolerance int64, field string) error {
	if c.DurationMs < MinClipDurationMS || c.DurationMs > MaxClipDurationMS {
		return &ValidationError{
			Rule: RuleIntensity, Field: field + ".durationMs",
			Message: fmt.Sprintf("%d outside [%d, %d]", c.DurationMs, MinClipDurationMS, MaxClipDurationMS),
			Status:  400,
		}
	}
	if c.DurationMs < eff.DurationRange[0] || c.DurationMs > eff.DurationRange[1] {
		return &ValidationError{
			Rule: RuleIntensity, Field: field + ".durationMs",
			Message: fmt.Sprintf("%q duration %d outside [%d, %d]",
				c.Type, c.DurationMs, eff.DurationRange[0], eff.DurationRange[1]),
			Status: 400,
		}
	}
	if c.StartMs < 0 {
		return &ValidationError{
			Rule: RuleTime, Field: field + ".startMs",
			Message: fmt.Sprintf("negative start %d", c.StartMs),
			Status:  400,
		}
	}
	if pc.DurationMS <= 0 {
		return &ValidationError{
			Rule: RuleTime, Field: field + ".startMs",
			Message: "page duration is unknown",
			Hint:    "调用方必须提供页时长",
			Status:  400,
		}
	}
	limit := pc.DurationMS + tolerance
	if end := c.StartMs + c.DurationMs; end > limit {
		return &ValidationError{
			Rule: RuleTime, Field: field + ".durationMs",
			Message: fmt.Sprintf("ends at %dms, page limit %dms", end, limit),
			Hint:    fmt.Sprintf("startMs+durationMs ≤ %d", limit),
			Status:  400,
		}
	}
	return nil
}

func validateClipTrigger(c *Clip, segments map[string]struct{}, pc PageContext, tolerance int64, field string) error {
	switch {
	case c.Trigger == triggerEnter:
		return nil
	case strings.HasPrefix(c.Trigger, triggerSegment):
		id := strings.TrimPrefix(c.Trigger, triggerSegment)
		if id == "" {
			return invalid(RuleStructure, field+".trigger", "missing segment id", 400)
		}
		if _, ok := segments[id]; !ok {
			// V7：锚点失效不是"格式错误"，而是"讲稿已变更"——必须让用户知道是内容漂移，
			// 因此用 409 而不是 400，且不在这里改写为 onEnter（改写会静默丢失语义）。
			return &ValidationError{
				Rule: RuleSegment, Field: field + ".trigger",
				Message: fmt.Sprintf("segment %q no longer exists", id),
				Hint:    "讲稿已重新生成，请重新选择分段或改为页首触发",
				Status:  409,
			}
		}
		return nil
	case strings.HasPrefix(c.Trigger, triggerAtTime):
		raw := strings.TrimPrefix(c.Trigger, triggerAtTime)
		ms, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || ms < 0 {
			return invalid(RuleStructure, field+".trigger", "atTime requires a non-negative millisecond", 400)
		}
		if pc.DurationMS > 0 && ms > pc.DurationMS+tolerance {
			return &ValidationError{
				Rule: RuleTime, Field: field + ".trigger",
				Message: fmt.Sprintf("atTime %dms exceeds page %dms", ms, pc.DurationMS),
				Status:  400,
			}
		}
		return nil
	case c.Trigger == "":
		return invalid(RuleStructure, field+".trigger", "missing trigger", 400)
	default:
		return &ValidationError{
			Rule: RuleStructure, Field: field + ".trigger",
			Message: fmt.Sprintf("unknown trigger %q", c.Trigger),
			Hint:    "onEnter | withSegment:<id> | atTime:<ms>",
			Status:  400,
		}
	}
}

// validateClipParams 校验"已显式设置的参数"是否被该动效声明、是否在区间内。
//
// 零值视为未设置（Params 无指针字段）：这意味着显式传 offset:0 会被当作默认值，
// 是可接受的取舍——offset 的默认值本就接近 0，而为此把 Params 全改成指针会让
// 契约复杂一档、收益极低。
func validateClipParams(c *Clip, eff *Effect, field string) error {
	type fieldValue struct {
		name    string
		kind    string
		set     bool
		num     float64
		str     string
		boolean bool
	}
	values := []fieldValue{
		{name: "direction", kind: "enum", set: c.Params.Direction != "", str: c.Params.Direction},
		{name: "offset", kind: "number", set: c.Params.Offset != 0, num: c.Params.Offset},
		{name: "scaleFrom", kind: "number", set: c.Params.ScaleFrom != 0, num: c.Params.ScaleFrom},
		{name: "axis", kind: "enum", set: c.Params.Axis != "", str: c.Params.Axis},
		{name: "loop", kind: "bool", set: c.Params.Loop, boolean: c.Params.Loop},
	}
	for _, v := range values {
		if !v.set {
			continue
		}
		spec, ok := eff.Params[v.name]
		if !ok {
			return &ValidationError{
				Rule: RuleType, Field: field + ".params." + v.name,
				Message: fmt.Sprintf("effect %q does not accept %q", eff.ID, v.name),
				Status:  400,
			}
		}
		switch spec.Type {
		case "number":
			if v.kind != "number" {
				return invalid(RuleType, field+".params."+v.name, "expects a number", 400)
			}
			if v.num < spec.Min || v.num > spec.Max {
				return &ValidationError{
					Rule: RuleType, Field: field + ".params." + v.name,
					Message: fmt.Sprintf("%v outside [%v, %v]", v.num, spec.Min, spec.Max),
					Status:  400,
				}
			}
		case "enum":
			if v.kind != "enum" {
				return invalid(RuleType, field+".params."+v.name, "expects a string", 400)
			}
			found := false
			for _, allowed := range spec.Values {
				if allowed == v.str {
					found = true
					break
				}
			}
			if !found {
				return &ValidationError{
					Rule: RuleType, Field: field + ".params." + v.name,
					Message: fmt.Sprintf("%q not allowed", v.str),
					Hint:    strings.Join(spec.Values, ", "),
					Status:  400,
				}
			}
		case "bool":
			if v.kind != "bool" {
				return invalid(RuleType, field+".params."+v.name, "expects a boolean", 400)
			}
		}
	}
	return nil
}

func anchorHint(anchors map[string]Anchor) string {
	ids := make([]string, 0, len(anchors))
	for id := range anchors {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) > 20 {
		ids = ids[:20]
	}
	return strings.Join(ids, ", ")
}

func presetIDs() []string {
	presets := Catalog().Presets()
	ids := make([]string, 0, len(presets))
	for _, p := range presets {
		ids = append(ids, p.ID)
	}
	return ids
}

func clipIDs(clips []*Clip) string {
	ids := make([]string, 0, len(clips))
	for _, c := range clips {
		ids = append(ids, c.ID)
	}
	return strings.Join(ids, ", ")
}

// clipField 把内部元素 key 还原成可定位的字段路径，便于前端高亮。
func clipField(r *Revision, key string) string {
	if key == pageLevelTrackKey {
		return "tracks[page].clips"
	}
	for i, t := range r.Tracks {
		if t != nil && t.ElementID == key {
			return fmt.Sprintf("tracks[%d].clips", i)
		}
	}
	return "tracks"
}

type span struct {
	start int64
	end   int64
}

func intervalsOverlap(a, b *Clip) bool {
	return a.StartMs < b.StartMs+b.DurationMs && b.StartMs < a.StartMs+a.DurationMs
}

// unionLength 返回区间并集长度（重叠归一，V6 用）。
func unionLength(spans []span) int64 {
	if len(spans) == 0 {
		return 0
	}
	sorted := make([]span, len(spans))
	copy(sorted, spans)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].start != sorted[j].start {
			return sorted[i].start < sorted[j].start
		}
		return sorted[i].end < sorted[j].end
	})
	var total int64
	cur := sorted[0]
	for _, s := range sorted[1:] {
		if s.start > cur.end {
			total += cur.end - cur.start
			cur = s
			continue
		}
		if s.end > cur.end {
			cur.end = s.end
		}
	}
	total += cur.end - cur.start
	return total
}
