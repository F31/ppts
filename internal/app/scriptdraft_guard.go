// 讲稿生成：确定性质检与提示词补丁。
//
// 从 scriptdraft.go 按职责拆出（纯搬移，实现逐字未改）：这里的函数全部是
// 不依赖 LLM 的判据——实体修复指令、容忍提示、事实引用校验、思维链痕迹检测，
// 以及成稿过短/复读原文的判定。集中在一处的价值是：这些规则要能被单测逐条钉住，
// 混在编排代码里时很难说清「哪条规则失败了」。

package app

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/F31/ppts/internal/validation"
)

// guardInstructions 是第二轮（定点修补）的指令。
//
// 只列**具体违规项**，并要求最小改动：把违规清单精确摆到模型面前，比让它重新读一遍原文
// 更可预期——后者每轮都在重新猜，命不命中靠运气。抽成纯函数是为了在没有 LLM 的机器上
// 也能锁定「指令里必须出现哪些信息」。
func guardInstructions(report validation.Report) string {
	parts := []string{"以下是需要修正的文案，它违反了数字/单位/型号保持规则："}
	if missing := validation.FormatEntitiesHuman(report.Missing); len(missing) > 0 {
		parts = append(parts, "遗漏了原素材中的「"+strings.Join(missing, "、")+"」，必须补回；")
	}
	inserted := validation.FormatEntitiesHuman(report.Inserted)
	if len(inserted) > 0 {
		parts = append(parts, "出现了原素材没有的「"+strings.Join(inserted, "、")+"」，必须删除；")
	}
	if len(report.Missing) == 0 && len(report.Inserted) == 0 {
		// 调用方只在 !OK() 时才会走到这里；留一个无死角的兜底，避免指令成为半句话。
		parts = append(parts, "其中的数字、单位与型号与原素材不一致；")
	}
	parts = append(parts, "除这些实体外不要改动其它措辞与结构，只做最小必要修改。")
	// 内部标记与思考过程必须显式禁止：模型会把 number:3 当成正文内容原样抄进去。
	parts = append(parts, "直接输出修正后的讲解正文：不要解释修改过程，不要输出 number/date/model "+
		"这类内部标记，也不要把思考过程写进正文。")
	return strings.Join(parts, "")
}

// entityFixSuffix 只返回「必须保留的事实」这几句，没有违规项时为空。
//
// 用于与 rewriteGuardInstructions 拼接：先要求把素材重写成讲稿，
// 再点名哪些数字/单位/型号不能少、哪些不能凭空多出来。
func entityFixSuffix(report validation.Report) string {
	parts := make([]string, 0, 2)
	if missing := validation.FormatEntitiesHuman(report.Missing); len(missing) > 0 {
		parts = append(parts, "务必保留原素材中的「"+strings.Join(missing, "、")+"」，不能遗漏；")
	}
	if inserted := validation.FormatEntitiesHuman(report.Inserted); len(inserted) > 0 {
		parts = append(parts, "不要出现原素材没有的「"+strings.Join(inserted, "、")+"」。")
	}
	return strings.Join(parts, "")
}

// toleratedNote 把「按策略放行的插入数字」转成一句给用户看的提示。
//
// 为什么必须提示：这条路径是**明知可能数错仍放行**——模型写「4 类场景」时，校验器
// 无法确认页面真的只有 4 条。沉默放行等于让用户失去核对机会，与 A26 的"不得假成功"
// 只差一步；写进步骤原因，代价很小。
//
// 只提示 warn 模式：allow 表示运营方明确不要提示（见 rules.json 的 counts）。
func toleratedNote(r validation.Report, text string) string {
	if !r.HasWarnings() {
		return ""
	}
	policy := validation.CountsPolicy()
	counts, ordinals := make([]validation.Entity, 0, len(r.Warnings)), make([]validation.Entity, 0, 2)
	for _, e := range r.Warnings {
		if e.Form == validation.FormOrdinal {
			ordinals = append(ordinals, e)
			continue
		}
		counts = append(counts, e)
	}
	parts := make([]string, 0, 2)
	if len(counts) > 0 && policy.InsertedCounts == validation.ModeWarn {
		parts = append(parts, "新增"+strings.Join(validation.FormatEntitiesHuman(counts), "、")+
			numberContextHint(counts, text)+"，属于对页面条目的概括而非实测数值")
	}
	if len(ordinals) > 0 && policy.InsertedOrdinals == validation.ModeWarn {
		parts = append(parts, "新增序号"+strings.Join(validation.FormatEntitiesHuman(ordinals), "、")+
			numberContextHint(ordinals, text))
	}
	if len(parts) == 0 {
		return ""
	}
	return "提示：" + strings.Join(parts, "；") + "。已放行，请核对是否与页面一致。"
}

// numberContextHint 从产出里截取"新增数字"周围的原文（最多两条，各留 8 字上下文）。
//
// 降级原因如果只写「新增数字 3」，用户没法判断这是模型在数页面列出的条目
// （「4 类场景」），还是凭空编了一个数——前者该放宽规则，后者该换模型或手改。
func numberContextHint(inserted []validation.Entity, text string) string {
	runes := []rune(text)
	hints := make([]string, 0, 2)
	for _, e := range inserted {
		if e.Kind != "number" || len(hints) >= 2 {
			continue
		}
		// 中文数词的实体值是归一后的阿拉伯数字（「三个」→ 3），但正文里并没有 "3"。
		// 这种情况下必须按原文写法定位，否则会截取到毫不相干的另一个 3。
		needle := e.Text
		if e.Surface != "" {
			needle = e.Surface
		}
		idx := strings.Index(text, needle)
		if idx < 0 {
			continue
		}
		pre := len([]rune(text[:idx]))
		from := pre - 8
		if from < 0 {
			from = 0
		}
		to := pre + len([]rune(e.Text)) + 8
		if to > len(runes) {
			to = len(runes)
		}
		snippet := strings.TrimSpace(strings.ReplaceAll(string(runes[from:to]), "\n", " "))
		hints = append(hints, "…"+snippet+"…")
	}
	if len(hints) == 0 {
		return ""
	}
	return "（" + strings.Join(hints, "；") + "）"
}

// rewriteGuardInstructions 是"上一版根本不是讲稿"时的第二轮指令。
//
// 与 guardInstructions 的区别：那里是**定点修补**（只补/删实体，其余一字不动），
// 这里要求**重新写成讲稿**。用错指令会得到反向结果——对一份照抄的页面文字说
// "除这些实体外不要改动其它措辞与结构"，模型只会更坚决地照抄。
func rewriteGuardInstructions(echo bool) string {
	var b strings.Builder
	if echo {
		b.WriteString("上一版把页面文字原样照抄了，不是讲解稿。请把素材改写成可直接朗读的口语化讲稿：")
		b.WriteString("保留其中的数字、单位、型号与专有名词，")
		b.WriteString("但不要保留版式的分行、标题罗列与标签堆叠，要用连贯的句子讲出来。")
	} else {
		b.WriteString("上一版的正文不干净（混入了思考过程或内部标记）。请重新把素材写成可直接朗读的讲解稿：")
	}
	b.WriteString("直接输出讲稿正文，不要解释修改过程，不要输出 number/date/model 这类内部标记，")
	b.WriteString("也不要把思考过程写进正文。")
	return b.String()
}

// factCitationPat 匹配讲稿正文中的事实引用标记：〔fact-xx〕。
//
// **允许未闭合**：推理型模型可能恰好停在标记中间（2026-11-02 实测：目标 60 秒的稿子
// 被截断在「〔fact-c」，长度只有 69 字）。只匹配闭合标记会把残片留在正文里，
// 用户照着念就会念出「左方括号 fact 横线 c」。残片只剥离、不计入引用集合。
var factCitationPat = regexp.MustCompile(`〔fact-[a-z0-9-]*〕?`)

// extractFactCitations 从模型产出中提取并剥离事实引用标记，返回纯正文
// 与引用的 ID 集合（保序、去重）。未匹配任何标记 = 纯正文原样返回。
//
// 只剥离两种情况：① 闭合且 ID 合法；② 停在文末的合法残片（真截断）。
// 「〔fact-〕」「〔fact-©〕」「〔fact-a b〕」这类畸形标记一律原样保留——
// 宁可漏判，也不要把用户正文里的方括号内容误删。
func extractFactCitations(text string) (clean string, cited []string) {
	seen := map[string]bool{}
	cited = []string{}
	var b strings.Builder
	last := 0
	for _, loc := range factCitationPat.FindAllStringIndex(text, -1) {
		m := text[loc[0]:loc[1]]
		closed := strings.HasSuffix(m, "〕")
		id := strings.TrimSuffix(strings.TrimPrefix(m, "〔"), "〕")
		if id == "" {
			continue
		}
		inner := strings.TrimPrefix(id, "fact-")
		if !factCitationIDOK(inner) {
			continue
		}
		truncatedTail := !closed && strings.TrimSpace(text[loc[1]:]) == ""
		if !closed && !truncatedTail {
			continue // 行中间未闭合 → 更像畸形标记，保留原文
		}
		b.WriteString(text[last:loc[0]])
		last = loc[1]
		if closed && !seen[id] {
			seen[id] = true
			cited = append(cited, id)
		}
	}
	b.WriteString(text[last:])
	return strings.TrimSpace(b.String()), cited
}

// factCitationIDOK 校验 fact- 后缀只含小写字母/数字/连字符（与 factID/
// structured fact 的合法字符集一致）。
func factCitationIDOK(suffix string) bool {
	for _, r := range suffix {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return suffix != ""
}

// unknownFactCitations 返回 cited 中不属于 available 的 ID（幻觉引用）。
// 空 available 时任何引用都视为无依据（但有提示词约束，正常不会发生）。
func unknownFactCitations(cited []string, available []draftFact) []string {
	if len(cited) == 0 {
		return nil
	}
	known := make(map[string]bool, len(available))
	for _, f := range available {
		known[f.ID] = true
	}
	unknown := make([]string, 0, len(cited))
	for _, id := range cited {
		if !known[id] {
			unknown = append(unknown, id)
		}
	}
	return unknown
}

// factCitationInstruction 生成引用约束指令（仅在存在可引用 facts 时追加）：
// 引用必须真实、可选、且只允许出现在【结构化事实】给出的候选 ID 内。
func factCitationInstruction(available []draftFact) string {
	if len(available) == 0 {
		return ""
	}
	ids := make([]string, 0, len(available))
	for _, f := range available {
		ids = append(ids, f.ID)
	}
	return "若某句讲解确实依据某条【结构化事实】，可在句尾标注其编号（形如 〔fact-a〕）。" +
		"只能标注本页事实清单中真实存在的编号（" + strings.Join(ids, "/") + "），不得虚构。标注可选，宁缺毋滥；标注会用于溯源校验。"
}

// citationGuardInstructions 生成针对幻觉引用的第二轮修补指令。
func citationGuardInstructions(unknown []string) string {
	if len(unknown) == 0 {
		return ""
	}
	return "你标注的引用编号不存在于本页事实清单中（" + strings.Join(unknown, "、") +
		"），必须改成真实存在的编号或直接删除标注；只做最小必要修改，只输出修正后的正文。"
}

// draftText 产出单页讲稿正文，返回 (displayText, spokenText, degraded, error)。
//
// degraded=true 表示数字/单位/型号校验两轮仍不过，已**保守回退为原始素材原文**（未经 AI 加工）：
// 落库的是页面要点片段而非讲解稿。该标志必须向上传递——当作成功会让界面把要点片段报告成
// "成稿完成"（A26：不得假成功）。
// internalEntityLiteralPat 匹配**只应存在于校验器内部**的实体标记（number:3 / date:2026-01-01）。
// 它们一旦出现在正文里，说明模型把修补指令当成了要复述的内容。
var internalEntityLiteralPat = regexp.MustCompile(`(?i)\b(?:number|date|model)\s*:`)

// chainOfThoughtMarkers 是"模型把思考过程写进正文"的典型片段。宁可漏判也不能误杀，
// 所以只收录语义上不可能出现在正常讲解稿里的句子开头。
var chainOfThoughtMarkers = []string{
	"The user wants", "The user says", "The user is asking", "Let me ",
	"I need to ", "I'm realizing", "Looking at the", "I should ",
	"用户要求我", "用户在要求我", "让我仔细阅读", "我应该假设", "我在尝试理解",
}

// draftOutputSuspect 判断模型产出是不是**不可用产出**：内部实体标记泄漏，或正文里
// 混入了思考过程。这类文本即便数字集合恰好对得上，也绝不能落库——它看起来像稿子，
// 实际是模型写完稿之前的中间状态，用户拿到会直接念出来（A26：不得假成功）。
//
// language 用于限定英文占比检查：英文讲稿本来就以英文为主，只在中文目标下判"英文过多"。
func draftOutputSuspect(language, text string) bool {
	if internalEntityLiteralPat.MatchString(text) {
		return true
	}
	for _, marker := range chainOfThoughtMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(language)), "zh") {
		if letters, total := asciiLetterCount(text); total > 200 && float64(letters)/float64(total) > 0.5 {
			return true
		}
	}
	return false
}

// draftTooShort 判断产出是否明显短于目标时长——即"残稿"。
//
// 中文讲解约 4 字/秒，这里按 2 字/秒取下限：只拦明显不完整的产出
// （2026-11-02 实测：目标 60 秒的稿子只落库 69 字，模型在 800 token 预算里
// 把大部分花在了思考上），不评判风格是否精炼。targetSeconds<=0 时不检查。
func draftTooShort(text string, targetSeconds int) bool {
	if targetSeconds <= 0 {
		return false
	}
	return utf8.RuneCountInString(strings.TrimSpace(text)) < targetSeconds*2
}

// draftEchoesSource 判断产出是不是**把页面文字原样回显**。
//
// 2026-11-02 真机：slide-266 落库的"讲稿"就是源形状按行拼接的结果
// （MULTI-PLATFORM / Web / 桌面端…每行原封不动，只少了页码），却记为 success。
// 实体校验对它毫无办法——逐字相同的文本不可能漏事实，于是"没改写"反而成了
// 最安全的通过方式。这不是成稿，按 A26 不得报成功。
//
// 两条判据：
//  1. 归一化（去空白/标点、统一小写）后与素材完全一致；
//  2. 素材至少 3 行，且八成以上的行在产出里逐行原样出现——版式结构被照搬。
//
// 素材不足 20 字时不做判据 1：封面单标题页润色后可能确实只剩一句话。
func draftEchoesSource(source, out string) bool {
	srcNorm := normalizeDraftText(source)
	if len([]rune(srcNorm)) >= 20 && srcNorm == normalizeDraftText(out) {
		return true
	}
	lines := nonEmptyLines(source)
	if len(lines) < 3 {
		return false
	}
	outLines := make(map[string]bool, len(lines))
	for _, l := range nonEmptyLines(out) {
		outLines[normalizeDraftText(l)] = true
	}
	hit := 0
	for _, l := range lines {
		if outLines[normalizeDraftText(l)] {
			hit++
		}
	}
	return float64(hit)/float64(len(lines)) >= 0.8
}

// normalizeDraftText 只保留字母与数字（含汉字），用于"是否同一段文字"的比较。
// 标点与空白不计：模型照抄时常常改写标点、把两行合并成一行。
func normalizeDraftText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}
func nonEmptyLines(s string) []string {
	out := make([]string, 0, 8)
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// asciiLetterCount 统计非空白字符数与其中的 ASCII 字母数，用于粗略判断语言是否跑偏。
func asciiLetterCount(text string) (letters, total int) {
	for _, r := range text {
		if unicode.IsSpace(r) {
			continue
		}
		total++
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			letters++
		}
	}
	return letters, total
}
