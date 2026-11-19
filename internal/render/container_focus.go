package render

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/F31/ppts/internal/animation"
)

// addContainerOverviews 给有成员焦点的数据容器（表格/图表）补一条总览焦点。
//
// 表格行、图表柱子本身已经有逐成员焦点；缺的是“表头/图例开始讲”的那一段。
// 这里不引入新的领域模型：容器还是 Element，总览还是 FocusSpan，后续新增
// 图表/架构图只需要扩展 isFocusContainer 和成员前缀规则。
func (c *Compiler) addContainerOverviews(in PageInput, focus []FocusSpan) []FocusSpan {
	if len(focus) == 0 || len(in.Elements) == 0 {
		return focus
	}
	cw, ch := c.opt.Canvas.Width, c.opt.Canvas.Height
	for _, el := range in.Elements {
		if !isFocusContainer(el.Kind) || el.Bounds == nil || el.ID == "" {
			continue
		}
		firstMember, memberIdx := int64(-1), -1
		already := false
		for i := range focus {
			if focus[i].ElementID == el.ID {
				already = true
				break
			}
			if !strings.HasPrefix(focus[i].ElementID, el.ID+"-") {
				continue
			}
			if firstMember < 0 || focus[i].StartMs < firstMember {
				firstMember = focus[i].StartMs
				memberIdx = i
			}
		}
		if already || firstMember < 0 {
			continue
		}
		start, ok := containerTextStart(in, el)
		if !ok || start <= 0 || start >= firstMember {
			continue
		}
		rect, _, ok, _ := boxOf(el.Bounds, c.scale, cw, ch)
		if !ok {
			continue
		}
		veil := focus[memberIdx].VeilAlpha
		if veil == 0 {
			veil = bakedVeilAlpha(1)
		}
		focus = append(focus, FocusSpan{
			StartMs:   start,
			EndMs:     firstMember,
			ElementID: el.ID,
			Rect:      &rect,
			FadeMs:    focusFadeDefaultMS,
			VeilAlpha: veil,
			group:     el.ID,
		})
	}
	sort.SliceStable(focus, func(i, j int) bool {
		if focus[i].StartMs != focus[j].StartMs {
			return focus[i].StartMs < focus[j].StartMs
		}
		return focus[i].ElementID < focus[j].ElementID
	})
	// 总览插进两段之间后，上一段必须放手，否则圈选会叠到总览上。
	for i := 1; i < len(focus); i++ {
		if focus[i-1].EndMs == 0 || focus[i-1].EndMs > focus[i].StartMs {
			if focus[i-1].StartMs < focus[i].StartMs {
				focus[i-1].EndMs = focus[i].StartMs
			}
		}
	}
	return focus
}

func isFocusContainer(kind string) bool {
	return kind == animation.KindTable || kind == animation.KindChart
}

// containerTextStart 用容器第一行文本（表头/图表标题）在字幕里的位置换算时间。
// 文本锚点只依赖页自己的 DisplayText 和字级钟，不写死“时间/节点”等词。
func containerTextStart(in PageInput, el Element) (int64, bool) {
	needle := compactRunes(firstTextLine(el.Text))
	if needle == "" {
		return 0, false
	}
	best := int64(-1)
	for seg, text := range in.SegmentTexts {
		starts := in.SegmentCharStarts[seg]
		if len(starts) == 0 || text == "" {
			continue
		}
		compact, indexes := compactRunesWithIndex(text)
		pos := strings.Index(compact, needle)
		if pos < 0 {
			continue
		}
		runePos := utf8.RuneCountInString(compact[:pos])
		if runePos < 0 || runePos >= len(indexes) || indexes[runePos] >= len(starts) {
			continue
		}
		ms := starts[indexes[runePos]]
		if ms < 0 {
			continue
		}
		if best < 0 || ms < best {
			best = ms
		}
	}
	return best, best >= 0
}

func firstTextLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

func compactRunesWithIndex(s string) (string, []int) {
	var b strings.Builder
	indexes := make([]int, 0, len(s))
	for i, r := range []rune(s) {
		if unicode.IsSpace(r) {
			continue
		}
		b.WriteRune(r)
		indexes = append(indexes, i)
	}
	return b.String(), indexes
}
