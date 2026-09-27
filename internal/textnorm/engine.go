// Package textnorm 是 TTS 文本规范化引擎（V2.8 §3）。
//
// 单一职责：把一段讲稿文本规范化为「送 TTS 的朗读文本 + 结构化停顿事件」，
// 并导出 StripMarkers 供字幕侧派生干净的显示文本。
//
// 设计要点：
//   - 唯一公开入口 Engine.Run；Scanner 归引擎内部，只识别显式定界符（〔读：x〕/‖），不切词；
//   - 读音/停顿 span 由 Scanner 结构性短路，不经过规则表；
//   - 规则表只处理「普通 span」；RegisterRule 后必须 Build() 冻结，冻结后不可再注册；
//   - 该包零 ppts 依赖，只使用标准库（并发安全由 Build 冻结保证）。
package textnorm

import (
	"math"
	"sort"
	"strings"
)

// SpanKind 是 Scanner 输出的 span 类别。
type SpanKind int

const (
	// SpanKindText 普通文本 span：进入规则表处理。
	SpanKindText SpanKind = iota
	// SpanKindReading 读音 span（〔读：x〕）：直接产出读音文本，不经规则表。
	SpanKindReading
	// SpanKindPause 停顿 span（‖）：产出 Pause 事件，输出文本不含 ‖。
	SpanKindPause
)

// Pause 是结构化停顿产出（调用方映射为 tts.SpeechControl.Pauses）。
type Pause struct {
	AfterRunes int // 停在输出文本第 N 个 rune 之后
	DurationMS int // 期望停顿毫秒数
}

// IndexMap 描述受规范化影响字符的 原文本位置→派生文本位置 对应（V2.8 §7.5 时间域 A）。
//
// 原文本 = Run 的输入（SpokenText，含标记）；派生文本 = Result.Text（effectiveText，送 TTS）。
// 段覆盖"位置发生偏移"的区域（标记剥离/展开、规则改写）；段间空隙补齐恒等段，
// 因此任意原文本位置都落在某段内（MapSrcToDst 恒可映射）。无任何偏移时为 nil（字数一致）。
type IndexMap struct {
	Segments []IndexSeg // 原文本 [SrcStart,SrcEnd) → 派生文本 [DstStart,DstEnd)，按 SrcStart 升序
}

// IndexSeg 一段 原文本→派生文本 的位置对应（rune 计数，半开区间）。
type IndexSeg struct {
	SrcStart int // 原文本 rune 起点
	SrcEnd   int // 原文本 rune 终点（不含）
	DstStart int // 派生文本 rune 起点
	DstEnd   int // 派生文本 rune 终点（不含）
}

// Empty 是否无段：nil 或零段均视为"字数一致，无需精确映射"。
func (m *IndexMap) Empty() bool { return m == nil || len(m.Segments) == 0 }

// MapSrcToDst 把原文本 rune 位置映射到派生文本 rune 位置。
// 越界返回 (0,false)；其余任意位置都应落在段内（构建时补全恒等段）。
func (m *IndexMap) MapSrcToDst(src int) (int, bool) {
	if m == nil {
		return 0, false
	}
	for _, s := range m.Segments {
		if s.SrcStart <= src && src < s.SrcEnd {
			if s.SrcEnd == s.SrcStart {
				return s.DstStart, true
			}
			if s.SrcEnd-s.SrcStart == s.DstEnd-s.DstStart {
				// 同长（恒等或等长改写）：段内线性保持。
				return s.DstStart + (src - s.SrcStart), true
			}
			// 异长（标记剥离/数值展开）：段内按比例估计（段通常是单个标记/一个数字，有界）。
			if s.DstEnd <= s.DstStart {
				return s.DstStart, true
			}
			span := s.SrcEnd - s.SrcStart
			frac := float64(src-s.SrcStart) / float64(span)
			d := s.DstStart + int(math.Round(frac*float64(s.DstEnd-s.DstStart)))
			if d >= s.DstEnd {
				d = s.DstEnd - 1
			}
			return d, true
		}
	}
	return 0, false
}

// Result 是 Run 的一次产出：规范化后的文本 + 结构化停顿 + （可选）字对不齐索引。
type Result struct {
	Text     string
	Pauses   []Pause
	IndexMap *IndexMap
}

// Rule 是一个函数，不是一个 interface。
// 返回 (输出文本, 是否命中)。命中即短路，不再走后续规则。
// 语义契约：ctx.Text 是一个【不含标记的普通 span】；需要子串粒度的规则自行扫描该 span。
type Rule func(*Context) (string, bool)

// Context 携带一次规则调用的全部共享状态；值类型，按指针传递。
type Context struct {
	Text     string // 输入 span 的完整文本（普通文本）
	OrigText string // 该 span 的原始文本（未被任何规则改写）；供规则做「原始边界」判定
	Lang     string // 语言（ppts 传 snapshot.Language，如 "zh-CN"）
	SpanKind SpanKind
	Raw      string // 读音 span 内容，仅 Reading 有效
	// Record 供规则上报"span 内子段"的原→派生位置对应（span 局部坐标，引擎再平移）。
	// 可选：不上报时，引擎对整段改写按规则输出字长变化记录一个 span 级段（比例近似）。
	Record func(seg IndexSeg)
}

type namedRule struct {
	name     string
	priority int
	apply    Rule
	mask     ruleMask // Build 时登记：0=无条件
}

// ruleMask 表示规则适用的文本特征位掩码；0 表示无条件（总是适用）。
type ruleMask uint8

const (
	// maskHasDigit 文本含 ASCII 数字（数值读法类规则的输入特征）。
	maskHasDigit ruleMask = 1 << iota
)

// textMask 计算一段文本的特征掩码，供 processSpan 快筛使用。
func textMask(s string) ruleMask {
	var m ruleMask
	if asciiDigitP(s) {
		m |= maskHasDigit
	}
	return m
}

// Engine 是唯一的规范化引擎：有序规则表 + 内置 Scanner。
// 构造后必须 Build() 冻结才能被并发安全地调用。
type Engine struct {
	rules        []namedRule
	defaultPause int
	frozen       bool
}

// Option 是 Engine 构造的可选项。
type Option func(*Engine)

// WithPauseDuration 配置默认停顿毫秒数（默认 300）。
func WithPauseDuration(ms int) Option {
	return func(e *Engine) {
		if ms > 0 {
			e.defaultPause = ms
		}
	}
}

// New 创建引擎。默认注册内置规则（见 AddDefaultRules），随后必须 Build()。
func New(opts ...Option) *Engine {
	e := &Engine{defaultPause: 300}
	for _, o := range opts {
		o(e)
	}
	return e
}

// RegisterRule 是唯一扩展点。内置规则与用户规则同走此路。
// 冻结（Build/首次 Run）之后调用会 panic。
func (e *Engine) RegisterRule(name string, priority int, r Rule) {
	e.register(name, priority, r, 0)
}

// RegisterDigitRule 注册需要"文本含 ASCII 数字"才可能命中的规则
// （数值读法类，见 NumberRule）。除额外登记 maskHasDigit 快筛掩码外，语义同 RegisterRule。
// 冻结之后调用会 panic。
func (e *Engine) RegisterDigitRule(name string, priority int, r Rule) {
	e.register(name, priority, r, maskHasDigit)
}

func (e *Engine) register(name string, priority int, r Rule, mask ruleMask) {
	if e.frozen {
		panic("textnorm: RegisterRule after Build/Run (engine is frozen)")
	}
	if name == "" || r == nil {
		panic("textnorm: RegisterRule requires non-empty name and non-nil rule")
	}
	e.rules = append(e.rules, namedRule{name: name, priority: priority, apply: r, mask: mask})
	sort.SliceStable(e.rules, func(i, j int) bool {
		return e.rules[i].priority < e.rules[j].priority
	})
}

// Build 冻结规则表：此后 RegisterRule panic，Run 可安全并发。
func (e *Engine) Build() {
	e.frozen = true
}

// Run 是唯一公开入口：整段讲稿 → 规范化文本 + 停顿事件。
// Run 内部完成：Scan → 逐 span 处理 → Rejoin。
// 偏移追踪：标记剥离/展开与规则改写都会造成 原文本位置≠派生文本位置，
// 记录进 IndexMap；无任何偏移时 IndexMap 为 nil（调用方回落比例近似）。
func (e *Engine) Run(text, lang string) Result {
	if !e.frozen {
		panic("textnorm: Run before Build (engine must be frozen first)")
	}
	var sb strings.Builder
	var pauses []Pause
	segs := []IndexSeg{}
	srcOff := 0 // 原文 rune 偏移
	dstOff := 0 // 派生文本 rune 偏移
	for _, sp := range scanSpans(text) {
		switch sp.Kind {
		case SpanKindText:
			srcEnd := srcOff + runeLen(sp.Raw)
			// 规则的子段由 processSpan 返回（span 局部坐标），在此平移成全局坐标。
			// Record 传非 nil 哨兵：processSpan 依其判"允许上报精配子段"，内部替换为收集器。
			ctx := &Context{Text: sp.Raw, OrigText: sp.Raw, Lang: lang, Record: func(IndexSeg) {}}
			out, spanSegs := e.processSpan(ctx)
			sb.WriteString(out)
			if len(spanSegs) > 0 {
				// 规则已上报精确子段：全部采纳（标记剥离等偏移仍是原子的 span 级段）。
				for _, seg := range spanSegs {
					segs = append(segs, IndexSeg{
						SrcStart: srcOff + seg.SrcStart, SrcEnd: srcOff + seg.SrcEnd,
						DstStart: dstOff + seg.DstStart, DstEnd: dstOff + seg.DstEnd,
					})
				}
			} else if out != sp.Raw && runeLen(out) != runeLen(sp.Raw) {
				// 无子段但整体字长变化：span 级段（比例近似）。
				segs = append(segs, IndexSeg{
					SrcStart: srcOff, SrcEnd: srcEnd,
					DstStart: dstOff, DstEnd: dstOff + runeLen(out),
				})
			}
			dstOff += runeLen(out)
			srcOff = srcEnd
		case SpanKindReading:
			// 〔读：x〕：原文 rune [srcOff, srcOff+len+4) → 派生 [dstOff, dstOff+len)。
			segSrcEnd := srcOff + runeLen(readingOpen) + runeLen(sp.Raw) + runeLen(readingClose)
			if runeLen(sp.Raw) != segSrcEnd-srcOff {
				segs = append(segs, IndexSeg{
					SrcStart: srcOff, SrcEnd: segSrcEnd,
					DstStart: dstOff, DstEnd: dstOff + runeLen(sp.Raw),
				})
			}
			sb.WriteString(sp.Raw)
			srcOff = segSrcEnd
			dstOff += runeLen(sp.Raw)
		case SpanKindPause:
			// ‖：原文 rune [srcOff, srcOff+1) → 派生空区间。
			segs = append(segs, IndexSeg{
				SrcStart: srcOff, SrcEnd: srcOff + runeLen(pauseMark),
				DstStart: dstOff, DstEnd: dstOff,
			})
			pauses = append(pauses, Pause{AfterRunes: dstOff, DurationMS: e.defaultPause})
			srcOff += runeLen(pauseMark)
		}
	}
	return Result{Text: sb.String(), Pauses: pauses, IndexMap: resolveIndexMap(segs, runeLen(text))}
}

// resolveIndexMap 组装 IndexMap：有偏移段时补全恒等段（空隙映射到自身），保证任意
// 原文本位置都落进某段；完全无偏移时返回 nil。
func resolveIndexMap(segs []IndexSeg, totalSrc int) *IndexMap {
	if len(segs) == 0 {
		return nil
	}
	sort.SliceStable(segs, func(i, j int) bool { return segs[i].SrcStart < segs[j].SrcStart })
	var merged []IndexSeg
	srcCursor := 0
	dstCursor := 0
	for _, s := range segs {
		// 恒等填补 [srcCursor, s.SrcStart)。
		if s.SrcStart > srcCursor {
			gapLen := s.SrcStart - srcCursor
			merged = append(merged, IndexSeg{SrcStart: srcCursor, SrcEnd: s.SrcStart, DstStart: dstCursor, DstEnd: dstCursor + gapLen})
			dstCursor += gapLen
		}
		// 跳过重叠（防御：不同 span 不应重叠，但保险起见从当前游标续段）。
		if s.SrcEnd <= srcCursor {
			continue
		}
		// 若段起点落后于游标（理论不发生），截断到游标。
		start := s.SrcStart
		dStart := s.DstStart
		if start < srcCursor {
			advance := srcCursor - start
			start = srcCursor
			dStart = s.DstStart + advance
		}
		merged = append(merged, IndexSeg{SrcStart: start, SrcEnd: s.SrcEnd, DstStart: dStart, DstEnd: s.DstEnd})
		srcCursor = s.SrcEnd
		dstCursor = s.DstEnd
	}
	// 尾随恒等。
	if srcCursor < totalSrc {
		gapLen := totalSrc - srcCursor
		merged = append(merged, IndexSeg{SrcStart: srcCursor, SrcEnd: totalSrc, DstStart: dstCursor, DstEnd: dstCursor + gapLen})
	}
	return &IndexMap{Segments: merged}
}

// processSpan 对单个普通 span 按优先级顺序叠加快带各层规则。
//
// 语义（V2.8 §3.4 分层）：不同层规则作用同一 span 的**不同子串**（10-19 词典替换
// 专有名词、20-29 数值展开数字），必须叠加而非首个命中短路——否则词典命中后数值
// 规则永不执行（CUDA 正确但要 2024 展开就会失败）。规则输出作为后续规则输入
// 继续处理，直到全部层遍历完。
//
// IndexMap 坐标约定：返回的软段是 span 局部坐标（调用方平移成全局）；仅当本规则
// 之前没有任何规则改变长度时才上报——否则源坐标已不对齐 span 原文，弃用精配，
// 由调用方按 span 级比例段处理（V2.8 §7.5"无 IndexMap 时回落比例近似"）。
func (e *Engine) processSpan(ctx *Context) (string, []IndexSeg) {
	if len(e.rules) == 0 {
		return ctx.Text, nil
	}
	out := ctx.Text
	lengthChanged := false
	var softSegs []IndexSeg
	spanMask := textMask(ctx.Text)
	for _, r := range e.rules {
		if r.mask != 0 && r.mask&spanMask == 0 {
			// 快筛：本 span 不具该规则要求的文本特征（如无 ASCII 数字），跳过。
			continue
		}
		ruleCtx := *ctx
		ruleCtx.Text = out
		// 前序规则未改长度时，允许本规则上报精确子段（坐标对齐 span 原文）。
		if !lengthChanged && ctx.Record != nil {
			var cur []IndexSeg
			ruleCtx.Record = func(seg IndexSeg) { cur = append(cur, seg) }
			next, hit := r.apply(&ruleCtx)
			ruleCtx.Record = nil
			if hit && next != out {
				softSegs = append(softSegs, cur...)
				if runeLen(next) != runeLen(out) {
					lengthChanged = true
				}
				out = next
			}
			continue
		}
		// 前序已改长度：禁止上报，纯顺序处理（后续规则输出不参与精配）。
		ruleCtx.Record = nil
		next, hit := r.apply(&ruleCtx)
		if !hit || next == out {
			continue
		}
		if runeLen(next) != runeLen(out) {
			lengthChanged = true
		}
		out = next
	}
	return out, softSegs
}

// StripMarkers 剥离读音/停顿标记、保留可读内容的纯函数，供 DisplayText 派生使用。
// 与 Run 内部 Scanner 共用同一个 span 语法来源（scanSpans），因此两端行为永不漂移。
// 剥离语义与 Run 完全一致：〔读：x〕展开为 x；‖ 移除；未闭合标记原样保留（不丢字）。
func StripMarkers(text string) string {
	out, _ := StripMarkersIndexed(text)
	return out
}

// StripMarkersIndexed 剥离标记并返回每个输出 rune 在原文本中的来源 rune 下标。
// 长度与输出文本一致；对 ‖（无输出）不产生条目。供 IndexMap 组合（显示文本→朗读文本）使用。
func StripMarkersIndexed(text string) (string, []int) {
	var sb strings.Builder
	var srcIdx []int
	srcOff := 0
	for _, sp := range scanSpans(text) {
		switch sp.Kind {
		case SpanKindText:
			for j := 0; j < runeLen(sp.Raw); j++ {
				sb.WriteRune([]rune(sp.Raw)[j])
				srcIdx = append(srcIdx, srcOff+j)
			}
		case SpanKindReading:
			for j := 0; j < runeLen(sp.Raw); j++ {
				sb.WriteRune([]rune(sp.Raw)[j])
				srcIdx = append(srcIdx, srcOff+runeLen(readingOpen)+j)
			}
		case SpanKindPause:
			// 无输出
		}
		srcOff += sourceSpanLen(sp)
	}
	return sb.String(), srcIdx
}

// sourceSpanLen 返回 span 在原文本中占用的 rune 数。
func sourceSpanLen(sp span) int {
	switch sp.Kind {
	case SpanKindReading:
		return runeLen(readingOpen) + runeLen(sp.Raw) + runeLen(readingClose)
	case SpanKindPause:
		return runeLen(pauseMark)
	default:
		return runeLen(sp.Raw)
	}
}

// DisplayToEffective 把引擎 Run 的结果组合成「显示文本 rune → 朗读文本 rune」的映射表。
//
// 必须以引擎 Run 的输入原文（SpokenText，可含标记）调用：IndexMap 的源坐标是原文坐标，
// 显示文本 = StripMarkers(原文)。返回 nil 表示无精确映射（字数一致或标记仅剥离，
// 调用方可回落比例近似）；返回切片时 disp[i] 对应 effectiveText 的 rune 下标。
func DisplayToEffective(original string, res Result) []int {
	if res.IndexMap == nil || len(res.IndexMap.Segments) == 0 {
		return nil
	}
	_, srcIdx := StripMarkersIndexed(original)
	if len(srcIdx) == 0 {
		return []int{}
	}
	out := make([]int, len(srcIdx))
	for i, s := range srcIdx {
		d, ok := res.IndexMap.MapSrcToDst(s)
		if !ok {
			return nil
		}
		out[i] = d
	}
	return out
}

// runeLen 按 rune 计数（中文场景与"字符数"一致；AfterRunes 契约以此为准）。
func runeLen(s string) int { return len([]rune(s)) }

// span 是 Scanner 的输出单元。
type span struct {
	Kind SpanKind
	Raw  string // Text/Reading 时的内容
}

const (
	readingOpen  = "〔读："
	readingClose = "〕"
	pauseMark    = "‖"
)

// scanSpans 是 Scanner 核心：按显式定界符把整段文本切成 span 序列。
// 只识别显式定界符，不做任何中文分词。未闭合的读音标记整体视为普通文本（不丢字）。
func scanSpans(text string) []span {
	var out []span
	rest := text
	for rest != "" {
		if strings.HasPrefix(rest, pauseMark) {
			out = append(out, span{Kind: SpanKindPause})
			rest = rest[len(pauseMark):]
			continue
		}
		if strings.HasPrefix(rest, readingOpen) {
			if closePos := strings.Index(rest, readingClose); closePos >= 0 {
				raw := rest[len(readingOpen):closePos]
				out = append(out, span{Kind: SpanKindReading, Raw: raw})
				rest = rest[closePos+len(readingClose):]
				continue
			}
			// 未闭合：不识别为标记，整个前缀回归普通文本（保留原文，不丢字）。
			out = append(out, span{Kind: SpanKindText, Raw: rest[:len(readingOpen)]})
			rest = rest[len(readingOpen):]
			continue
		}
		// 找下一处标记起点，一次吞入最长普通 span。
		next := indexAny(rest, pauseMark, readingOpen)
		if next <= 0 {
			out = append(out, span{Kind: SpanKindText, Raw: rest})
			break
		}
		out = append(out, span{Kind: SpanKindText, Raw: rest[:next]})
		rest = rest[next:]
	}
	return out
}

// indexAny 返回 text 中任意一个 token 首次出现的下标；都不存在时返回 -1。
func indexAny(text string, tokens ...string) int {
	best := -1
	for _, tok := range tokens {
		if tok == "" {
			continue
		}
		if i := strings.Index(text, tok); i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	return best
}
