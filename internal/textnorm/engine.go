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

// Result 是 Run 的一次产出：规范化后的文本 + 结构化停顿。
type Result struct {
	Text   string
	Pauses []Pause
}

// Rule 是一个函数，不是一个 interface。
// 返回 (输出文本, 是否命中)。命中即短路，不再走后续规则。
// 语义契约：ctx.Text 是一个【不含标记的普通 span】；需要子串粒度的规则自行扫描该 span。
type Rule func(*Context) (string, bool)

// Context 携带一次规则调用的全部共享状态；值类型，按指针传递。
type Context struct {
	Text     string // 输入 span 的完整文本（普通文本）
	Lang     string // 语言（ppts 传 snapshot.Language，如 "zh-CN"）
	SpanKind SpanKind
	Raw      string // 读音 span 内容，仅 Reading 有效
}

type namedRule struct {
	name     string
	priority int
	apply    Rule
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
	if e.frozen {
		panic("textnorm: RegisterRule after Build/Run (engine is frozen)")
	}
	if name == "" || r == nil {
		panic("textnorm: RegisterRule requires non-empty name and non-nil rule")
	}
	e.rules = append(e.rules, namedRule{name: name, priority: priority, apply: r})
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
func (e *Engine) Run(text, lang string) Result {
	if !e.frozen {
		panic("textnorm: Run before Build (engine must be frozen first)")
	}
	var sb strings.Builder
	var pauses []Pause
	for _, sp := range scanSpans(text) {
		switch sp.Kind {
		case SpanKindText:
			sb.WriteString(e.processSpan(&Context{Text: sp.Raw, Lang: lang}))
		case SpanKindReading:
			sb.WriteString(sp.Raw)
		case SpanKindPause:
			pauses = append(pauses, Pause{AfterRunes: runeLen(sb.String()), DurationMS: e.defaultPause})
		}
	}
	return Result{Text: sb.String(), Pauses: pauses}
}

// processSpan 对单个普通 span 遍历规则表，命中即短路。
func (e *Engine) processSpan(ctx *Context) string {
	if len(e.rules) == 0 {
		return ctx.Text
	}
	for _, r := range e.rules {
		if out, hit := r.apply(ctx); hit {
			return out
		}
	}
	return ctx.Text
}

// StripMarkers 剥离读音/停顿标记、保留可读内容的纯函数，供 DisplayText 派生使用。
// 与 Run 内部 Scanner 共用同一个 span 语法来源（scanSpans），因此两端行为永不漂移。
// 剥离语义与 Run 完全一致：〔读：x〕展开为 x；‖ 移除；未闭合标记原样保留（不丢字）。
func StripMarkers(text string) string {
	var sb strings.Builder
	for _, sp := range scanSpans(text) {
		switch sp.Kind {
		case SpanKindText, SpanKindReading:
			sb.WriteString(sp.Raw)
		case SpanKindPause:
			// 停顿不进入显示文本
		}
	}
	return sb.String()
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
