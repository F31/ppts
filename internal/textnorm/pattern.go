package textnorm

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Pattern 是"声明式"规则（对标 itntext 的 tagger→verbalizer 架构，V3.0 §1.2）。
//
// 与函数闭包规则（Rule）不同，Pattern 描述的是"类别隔离"的规则：
//   - Match 识别待规范化的子串（tagger）；
//   - Verbalize 把命中子串按类别模板产出中文读法（verbalizer）。
//
// 用途：新增领域规则（百分比/日期/金额/单位/分数）只需声明"识别 + 产出"，
// 不再需要手写状态机扫描。Pattern 在普通 span 上迭代匹配，逐段产出并上报精确坐标。
type Pattern struct {
	// Category 是类别名（仅用于可观测/调试，不参与执行）。
	Category string
	// Priority 与 RegisterRule 同语义（低值先执行）。
	Priority int
	// Match 是识别待规范化子串的表达式；编译期预编译（Build 冻结）。
	Match *regexp.Regexp
	// Verbalize 把命中子串转中文读法。返回 (替换文本, 是否命中)。
	// 注意：替换文本可含 Match 未捕获的任意字符（模板），长度可与原文不同。
	Verbalize func(matched string, ctx *Context) (string, bool)
}

// RegisterPattern 注册一条声明式规则（V3.0 §1.2.2）。
// 语义：在普通 span 内迭代 Match 的匹配，逐个 verbalize 并上报精确子段。
// 与 DefinitionRule（整 span ReplaceAll）互补：词典类仍走 ReplaceAll，
// 声明式类别（percent/date/money/unit/fraction）走 Pattern。
//
// 冻结（Build/首次 Run）之后调用会 panic。
func (e *Engine) RegisterPattern(p Pattern) {
	if e.frozen {
		panic("textnorm: RegisterPattern after Build/Run (engine is frozen)")
	}
	if p.Match == nil || p.Verbalize == nil {
		panic("textnorm: RegisterPattern requires non-nil Match and Verbalize")
	}
	e.register(p.Category, p.Priority, patternRule(p), 0)
}

// patternRule 把 Pattern 包装成 Rule：在 ctx.Text 上迭代匹配，逐段替换并上报坐标。
//
// 语义（V3.0 §1.2.1，对标 itntext 的类别隔离）：
//   - 只作用于普通 span；
//   - 每个匹配段独立 verbalize，段间空隙恒等保留；
//   - 命中段通过 ctx.Record 上报精确 原→派生 坐标（span 局部坐标）。
//
// 注意：Pattern 与词典/数值规则的叠加语义由 processSpan 统一处理——
// Pattern 的匹配基于"当前阶段文本"，故若前序规则已改写 span，坐标对齐遵循
// processSpan 的 lengthChanged 纪律（前序改长度后本规则不再上报精配）。
func patternRule(p Pattern) Rule {
	return func(ctx *Context) (string, bool) {
		if ctx.SpanKind != SpanKindText {
			return "", false
		}
		if !p.Match.MatchString(ctx.Text) {
			return "", false
		}
		var sb strings.Builder
		changed := false
		lastEnd := 0
		dstCount := 0
		var segs []IndexSeg
		for _, loc := range p.Match.FindAllStringSubmatchIndex(ctx.Text, -1) {
			srcStart, srcEnd := runeOffset(ctx.Text, loc[0]), runeOffset(ctx.Text, loc[1])
			matched := ctx.Text[loc[0]:loc[1]]
			repl, ok := p.Verbalize(matched, ctx)
			if !ok {
				continue
			}
			// 空隙恒等保留（含未命中的匹配前的字符）。
			sb.WriteString(ctx.Text[lastEnd:loc[0]])
			dstCount += runeCount(ctx.Text[lastEnd:loc[0]])
			dstStart := dstCount
			sb.WriteString(repl)
			dstCount += runeLen(repl)
			dstEnd := dstCount
			segs = append(segs, IndexSeg{
				SrcStart: srcStart, SrcEnd: srcEnd,
				DstStart: dstStart, DstEnd: dstEnd,
			})
			lastEnd = loc[1]
			changed = true
		}
		if !changed {
			return "", false
		}
		// 尾部空隙恒等保留。
		sb.WriteString(ctx.Text[lastEnd:])
		if ctx.Record != nil {
			for _, s := range segs {
				ctx.Record(s)
			}
		}
		return sb.String(), true
	}
}

// runeOffset 把字节位置换算为 rune 位置（rune 坐标对 IndexMap 一致）。
// utf8.RuneCountInString 是 O(n) 但零分配（无需构造 rune 切片）。
func runeOffset(src string, bytePos int) int {
	if bytePos <= 0 {
		return 0
	}
	if bytePos >= len(src) {
		return runeLen(src)
	}
	return utf8.RuneCountInString(src[:bytePos])
}

// runeCount 便捷：字符串的 rune 数。
func runeCount(s string) int { return len([]rune(s)) }

// ReplacementPattern 构造数据驱动的「正则 → 固定模板」声明式规则（V3.0 §3.3 / M5）。
//
// 用途：运营后台可编辑的上下文替换（对标 itntext TSV 资源表）——模式与产出模板存 DB，
// 引擎侧无需代码即可新增规则。与手写 verbalizer 的类别 Pattern 互补：
// ReplacementPattern 是纯数据规则（regexp + replacement），无 Go 逻辑。
//
// pattern 为 Go 正则（支持捕获组），replacement 为产出模板（$1 / ${name} 等）。
// 语义：在普通 span 内迭代 pattern 匹配，逐段以 replacement 替换，并上报精确
// 原→派生 坐标（与 patternRule 一致，IndexMap 精确）。
//
// 非法正则返回 error（装配方应在启动/加载时报错，而非静默丢弃——R1 依赖不可用不能退回成功）。
func ReplacementPattern(pattern, replacement string, priority int) (Pattern, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return Pattern{}, err
	}
	return Pattern{
		Category: "context",
		Priority: priority,
		Match:    re,
		Verbalize: func(matched string, _ *Context) (string, bool) {
			return re.ReplaceAllString(matched, replacement), true
		},
	}, nil
}
