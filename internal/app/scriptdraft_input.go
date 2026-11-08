// 讲稿生成：输入选择与提示词素材。
//
// 从 scriptdraft.go 按职责拆出（纯搬移，实现逐字未改）：决定一页讲稿的输入究竟取
// 页面文字、讲稿备注还是自定义文本，并拼装页面级提示词素材。

package app

import (
	"sort"
	"strings"

	"github.com/F31/ppts/internal/narration"
)

// augmentDraftSource 把已抽取的原子事实（draftFact）汇入讲稿生成输入，返回两份基准：
//   - promptSource：发给模型的输入。在原素材后追加【结构化事实】块——表格/图表/图片/
//     备注等异构元素的事实带 [fact-xx] 编号供模型组织讲解，编号在讲稿正文中不得输出。
//   - validationSource：供数字/单位/型号守门校验（CheckPreserved）的基准。必须**与 prompt 对称**，
//     否则模型按事实引用了某个数字（如表格里的 128GB）而校验基准只有原素材 → 被判「新增实体」
//     违规。正是这段对称，也是 M11 的教训：提示词与校验器各说一套就会漂移。
//
// 注意：facts 是**结构化/视觉观察的快照**（slideInsight 缓存），不随每次生成重新提取——
// 校验永远对着同一份事实，避免「今天校验通过、明天重新调视觉模型又失败」的非稳定行为。
// PromptOnly 的事实（视觉场景描述等自由文本）只进 prompt、不进校验基准：它们是低置信度、
// 非确定性的辅助上下文，硬性要求模型复述其中的数字会误杀（M11）。
//
// 无事实时（纯文本页 / inputExisting / inputCustom）双基准都等于原素材，行为与以往完全一致。
func augmentDraftSource(source string, facts []draftFact) (promptSource string, validationSource string) {
	source = strings.TrimSpace(source)
	if len(facts) == 0 {
		return source, source
	}
	var prompt strings.Builder
	if source != "" {
		prompt.WriteString(source)
		prompt.WriteString("\n\n")
	}
	prompt.WriteString("【结构化事实】以下事实来自本页 PPT 的结构解析或视觉观察，仅用于组织讲解；方括号编号只用于内部溯源，不要在讲稿正文中输出。\n")
	values := make([]string, 0, len(facts)+1)
	if source != "" {
		values = append(values, source)
	}
	elementKinds := map[string]bool{}
	for _, fact := range facts {
		value := strings.TrimSpace(fact.Value)
		if value == "" {
			continue
		}
		prompt.WriteString("- [")
		prompt.WriteString(fact.ID)
		prompt.WriteString("] ")
		prompt.WriteString(value)
		prompt.WriteByte('\n')
		if !fact.PromptOnly {
			values = append(values, value)
		}
		if fact.ElementKind != "" {
			elementKinds[fact.ElementKind] = true
		}
	}
	// 页面级编排（第五阶段）：同一页含多个异构元素（表格/图表/图片/文本）时，
	// 引导模型按讲解价值组织成连贯段落，而不是逐个元素机械罗列。该指令只进
	// prompt、不进校验基准——它没有可校验的事实数字，混入会污染 CheckPreserved。
	if len(elementKinds) > 1 {
		prompt.WriteString("\n【编排】本页同时包含：")
		keys := make([]string, 0, len(elementKinds))
		for k := range elementKinds {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		prompt.WriteString(strings.Join(keys, "、"))
		prompt.WriteString("。请把它们组织成一段连贯讲解：先讲本页最想传达的核心结论，再按逻辑需要穿插各元素佐证；" +
			"同一页面上的次要元素一句话带过即可，不要把每个元素各讲一遍“然后我们看看……”式地机械罗列。")
	}
	// 按类型模板（NarrationTemplate 的轻量版）：给出该类型通用的讲解结构骨架，
	// 让模型按骨架组织而非自由发挥。结构说明不含可校验数字，故只进 prompt。
	tpl := elementTemplatePrompt(elementKinds)
	if tpl != "" {
		prompt.WriteString("\n")
		prompt.WriteString(tpl)
	}
	return strings.TrimSpace(prompt.String()), strings.TrimSpace(strings.Join(values, "\n"))
}

// elementTemplatePrompt 按页面含有的元素类型返回讲解结构骨架（模板层）。
// 每种类型只给"该讲什么、按什么顺序"，不给具体数字——数字由 facts 提供并被校验。
func elementTemplatePrompt(kinds map[string]bool) string {
	var parts []string
	if kinds["chart"] {
		parts = append(parts, "图表：优先讲趋势/对比/极值，而不是画面构图；结合数值事实讲“变化”与“谁最大/最小”。")
	}
	if kinds["table"] {
		parts = append(parts, "表格：优先讲结构含义（行列关系、表头语义）、关键列的比较与排名，而非逐格复述。")
	}
	if kinds["image"] {
		parts = append(parts, "图片：讲这张图展示的对象/场景及其与当前主题的关联；如有识别出的文字或对象，挑关键细节，不逐个描述。")
	}
	if kinds["code"] {
		parts = append(parts, "代码：讲这段代码做什么（意图）、关键实现方式及其与主题的关系，不要逐行解释语法。")
	}
	if kinds["notes"] {
		parts = append(parts, "备注：作为讲解主线素材使用，内容优先于版面元素。")
	}
	if len(parts) == 0 {
		return ""
	}
	return "【讲解结构】" + strings.Join(parts, "")
}

// SourceShapeText 是「原文素材」计算所需的形状最小视图（只用到种类与文字）。
//
// 单独定义而不是直接吃 project.Shape：成稿读的是 document.json 的最小视图
// （parsedShape），并排对照读的是 project.Document——两者字段不同，但
// 「什么算原文、按什么优先级取」必须**只有一份实现**，否则对照面板显示的
// "原文"会和成稿实际吃进去的素材不一致。
type SourceShapeText struct {
	Kind string
	Text string
}

// IsLayoutOnlyNumber 判断一个形状是否只是**版式编号**（页脚页码 "03"、目录序号 "12"），
// 而不是页面内容。判定刻意保守：纯 ASCII 数字、1~3 位、且不是标题形状。
// 4 位以上的纯数字（年份 2026、编号 10086）一律当作内容保留——误删页面数据的代价
// 比多留一个页码大得多。
//
// 为什么必须剔除：一键成稿以 page_content 为来源时，页脚页码会进入
// validation.ExtractEntities，被当成"必须保留的事实"。讲解稿不朗读页码，于是每页都
// 判 missing，两轮修补后回退原文（degraded）——这正是 2026-11-02 那次 14 页任务里
// 5 页降级的根因。
func IsLayoutOnlyNumber(kind, text string) bool {
	if strings.EqualFold(strings.TrimSpace(kind), "title") {
		return false
	}
	t := strings.TrimSpace(text)
	if t == "" || len(t) > 3 {
		return false
	}
	for i := 0; i < len(t); i++ {
		if t[i] < '0' || t[i] > '9' {
			return false
		}
	}
	return true
}

// PageSourceText 按页面级来源取文本，**并返回最终胜出的来源种类**（page/notes/custom）。
// source 指定时只取该来源对应文本；source=custom 用 custom 文本；source=notes 仅用备注。
// 这是页面级取值的唯一实现——文本与来源种类必须由同一处产生，
// 否则"返回的文本算在一处、来源判定写在另一处"会重新漂移。
func PageSourceText(shapes []SourceShapeText, notes, source, custom string) (string, string) {
	source = strings.TrimSpace(source)
	switch source {
	case "notes":
		return "notes", strings.TrimSpace(notes)
	case "custom":
		if t := strings.TrimSpace(custom); t != "" {
			return "custom", t
		}
		return "notes", strings.TrimSpace(notes)
	}
	var parts []string
	for _, sh := range shapes {
		if source == "title" && !strings.EqualFold(strings.TrimSpace(sh.Kind), "title") {
			continue
		}
		if source == "body" && strings.EqualFold(strings.TrimSpace(sh.Kind), "title") {
			continue
		}
		// 版式编号（页脚页码、目录序号）不是页面内容：讲解稿不会朗读"03"，
		// 若把它拼进校验基准，模型每页都"漏掉一个数字"，两轮修补后只能回退原文。
		if IsLayoutOnlyNumber(sh.Kind, sh.Text) {
			continue
		}
		if t := strings.TrimSpace(sh.Text); t != "" {
			parts = append(parts, t)
		}
	}
	text := strings.TrimSpace(strings.Join(parts, "\n"))
	if text == "" {
		// 形状文字为空 → 回退备注：来源种类也必须是备注，不能仍标成"页面文字"。
		return "notes", strings.TrimSpace(notes)
	}
	return "page", text
}

// pgInput 是 SourceShapeText 版本的页面级取值：把 parsedShape 投影成最小视图后
// 交给 PageSourceText，保证成稿与对照面板共用同一套来源优先级。
func pgInput(pg parsedPage, source, custom string) draftInput {
	shapes := make([]SourceShapeText, 0, len(pg.Shapes))
	for _, sh := range pg.Shapes {
		shapes = append(shapes, SourceShapeText{Kind: sh.Kind, Text: sh.Text})
	}
	kind, text := PageSourceText(shapes, pg.NotesText, source, custom)
	return draftInput{Kind: draftInputKind(kind), Text: text}
}

// pgInputForMode 在页面级来源之上叠加批量来源（sourceMode，来自一键成稿）。
func pgInputForMode(pg parsedPage, source, custom, sourceMode string) draftInput {
	switch strings.TrimSpace(sourceMode) {
	case "notes_first":
		if notes := strings.TrimSpace(pg.NotesText); notes != "" {
			return draftInput{Kind: inputNotes, Text: notes}
		}
		return pgInput(pg, "layout", "")
	case "notes_only":
		return draftInput{Kind: inputNotes, Text: strings.TrimSpace(pg.NotesText)}
	case "page_content":
		return pgInput(pg, "layout", "")
	default:
		return pgInput(pg, source, custom)
	}
}

// selectDraftInput 决定单页成稿的输入：**文本 + 它的实际来源**（Text 为空 = 该页没有可用素材，应跳过）。
// 返回 Kind 而不是只返回文本，是为了让提示词能据此描述输入（见 draftInstructions）；
// 若分成两个函数各判一次来源，两份判断迟早会不一致。
//
// **来源优先级是本函数的唯一职责**，也是曾经的缺陷所在：
//
//	① 用户显式指定的来源（批量 sourceMode，或页面级 source）永远优先——即使 overwrite=true，
//	   也不得用已有讲稿顶替用户选择的来源；
//	② 只有在「未显式指定来源」时才沿用历史行为：
//	   - 原文模式：取备注（无备注则不生成）；
//	   - overwrite=true：以当前讲稿为输入（单页「重新生成讲稿」= 润色现有稿）。
//
// 缺陷史（2026-09-22）：原实现先按来源算好页面文字，再用 `overwrite && rev != nil` 无条件
// 覆盖成旧讲稿。于是「一键成稿 · 来源=页面内容 · 覆盖全部」实际是把旧讲稿润色一遍，
// 页面文字从未进入提示词；产出看起来"没按页面内容生成"。
func selectDraftInput(pg parsedPage, source, custom, sourceMode string, mode narration.ScriptMode, overwrite bool, existing string) draftInput {
	explicit := strings.TrimSpace(sourceMode) != "" || strings.TrimSpace(source) != ""
	if !explicit {
		if mode == narration.ModeOriginal {
			return draftInput{Kind: inputNotes, Text: strings.TrimSpace(pg.NotesText)}
		}
		// 显式重新生成时，润色/AI 生成以用户当前讲稿为输入，而不是重新从 PPT 版面抽取。
		if overwrite {
			if current := strings.TrimSpace(existing); current != "" {
				return draftInput{Kind: inputExisting, Text: current}
			}
		}
	}
	return pgInputForMode(pg, source, custom, sourceMode)
}
