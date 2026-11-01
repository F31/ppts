package bind

import "strings"

// 本文件回答一个问题：**讲稿有没有把版面结构讲出来**。
//
// 为什么分档需要它：结构词消融实测（scripts/fidelity/ablate_structure.py）显示，
// 同一份有编号/卡片的讲义，讲稿含结构词时内容召回 0.972，把 13 处结构词删掉后掉到 0.667。
// 也就是说「版面有结构 ⇒ 预填可信」缺了一个前提：讲稿得把结构念出来。分档只读版面时，
// 「版面有编号、讲稿口语直讲」这一格会被判成结构化且不给低置信提示——恰好在真实的高风险
// 组合上沉默。
//
// 判定口径与三条规则**同一套正则**（ordinalRe / qtyMatch / dirRe / tableRefs / imageRefs /
// centerWords），不另造词表。这不是偷懒：分档说「有信号」而规则一条都绑不上，比不分档更糟。
// 代价是改规则会连带影响分档，这条耦合写进了 design。

// 结构词类别。按固定顺序返回，便于测试与调试时说清「命中的是哪一类」。
const (
	ScriptOrdinal = "ordinal"
	ScriptCount   = "count"
	ScriptSpatial = "spatial"
)

// scriptStructureKinds 返回讲稿命中的结构词类别（去重、固定顺序）。
//
// 返回类别而不是布尔：只看布尔时「第二条」这种序数形态无法与「分两条」区分，
// 而两者对规则的意义不同（前者逐条讲、后者是概括）。报告与测试要能分开看。
func scriptStructureKinds(segments []Segment) []string {
	var ordinal, count, spatial bool
	for _, seg := range segments {
		text := seg.Text
		if text == "" {
			continue
		}
		if !ordinal && ordinalRe.MatchString(text) {
			ordinal = true
		}
		// detectCount 与 countBind 同一口径：要求 2..12 条的精确概括。
		// 不能直接用 qtyMatch——「高一个点」「一个人」都会被它当成数量词点亮，
		// 而「一个X」不编码任何版面结构（版面不会有 1 项的列表）。
		if !count {
			if _, ok := detectCount(text); ok {
				count = true
			}
		}
		if !spatial {
			if dirRe.MatchString(text) ||
				containsAny(text, tableRefs) || containsAny(text, imageRefs) ||
				containsAny(text, centerWords) {
				spatial = true
			}
		}
	}
	kinds := make([]string, 0, 3)
	for _, k := range []struct {
		name string
		ok   bool
	}{{ScriptOrdinal, ordinal}, {ScriptCount, count}, {ScriptSpatial, spatial}} {
		if k.ok {
			kinds = append(kinds, k.name)
		}
	}
	return kinds
}

// hasScriptStructure 是分档用的布尔判定：讲稿至少命中一类结构词。
func hasScriptStructure(segments []Segment) bool {
	return len(scriptStructureKinds(segments)) > 0
}

func containsAny(text string, words []string) bool {
	for _, w := range words {
		if strings.Contains(text, w) {
			return true
		}
	}
	return false
}
