// Package validation contains deterministic guards for AI-generated narration text.
package validation

import (
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// 归一化（M11，2026-09-22）——把「同一实体的不同合法写法」收敛到同一个键。
//
// 背景：本包此前按**字面**比较实体，于是模型把 `12ms` 写成「12 毫秒」、把 `2026-09-13`
// 写成「2026 年 9 月 13 日」都会被判成「删掉了原实体 + 新增了实体」，两轮校验都不过，
// 整页回退原文（degraded）。实测 10 个用例里 8 例属此类**误杀**。
//
// 归一化只统一**写法**，不放松门禁：数量、单位、日期、型号仍必须全部照样出现；
// 「数字被改」（80GB→96GB）、「数字被删」（98 分→得分很高）、「原文无数字时模型自行编号」
// 依然判违规（见 entities_test.go 的守门用例）。
//
// 等价关系只声明一次（rules.json），提示词侧通过 FormatEquivalenceHint /
// FormatMeasureHint 取同一张表，避免出现「指令说可以这样写、校验器却拒绝」的漂移——
// M1/M3 的教训就是同一件事在两处各判一次、迟早不一致。
//
// 量词（M16，2026-09-22）：数量后面跟的中文量词按**语义家族**参与比较。
// 「3 页」与「3 个项目」是两件事（量词异族 → 判违规），而「3 个」与「3 条」是同一件事
// （同族 → 放行）。任一侧**没写**量词视为兼容——成稿常省略或改换说法，这与"事实被改"不同；
// 只有两侧都写了量词且不同族才算冲突。这样既不放过"页→个"，也不误杀"个→条"。

// numberPat 是数量本体：优先匹配带千分位的写法（1,000），否则普通十进制。
// 千分位必须进正则：`\d+` 遇到 "1,000" 会先咬住 "1"，再从左边的逗号后咬住 "000"，
// 于是一个数字被拆成两个实体（"1" 与 "000"），与目标的 "1000" 完全对不上。
const numberPat = `(?:\d{1,3}(?:,\d{3})+(?:\.\d+)?|\d+(?:\.\d+)?)`

var (
	// dateFullRe / dateYmRe：日期必须**先于**数量抽取匹配，并把匹配区间挖成等长空白，
	// 否则 `2026-09-13` 会被裸数字规则拆成 2026 / 09 / 13 三个噪声实体，而目标写成
	// 「2026 年 9 月 13 日」（旧正则对空格零容忍）时又整条匹配不上，双向误报。
	//
	// 只认「年+月（+日）」，**不认单独的年份**：单独的 2026 与「2026 年」语义相同，
	// 都应当是数量 2026，不该因为多了一个「年」字就变成另一种实体。
	// 空白容差用 [ \t]*（不跨行），使「2026-09-13」「2026 年 9 月 13 日」同键。
	dateFullRe = regexp.MustCompile(`\d{4}[ \t]*[-/.年][ \t]*\d{1,2}[ \t]*[-/.月][ \t]*\d{1,2}[ \t]*日?`)
	dateYmRe   = regexp.MustCompile(`\d{4}[ \t]*[-/.年][ \t]*\d{1,2}[ \t]*月`)
	dateNumRe  = regexp.MustCompile(`\d+`)

	numberUnitRe = buildNumberUnitRe()

	// multiplierRe：`x8` / `×8` 是「8 个」的计数写法，不是型号。把它折成裸数量，
	// 免得 modelRe 把 "x8" 当型号要求逐字保留（目标常写成「8 张 A100」→ 双向误报）。
	//
	// 这是**对称**归一（源与目标同一规则），所以不会把真实的数字改动放过去：
	// "x86" 与 "x64" 归一为 86 与 64，照样判违规。RE2 无后顾断言，
	// 故直接匹配整段并回填数字（`\b` 保证 "3x"、"100x8" 这类不被误折叠）。
	multiplierRe = regexp.MustCompile(`(?i)\bx(\d+)|×(\d+)`)

	// modelRe：字母打头且含数字的 token（A100、k8s、x86、PCIe 不匹配因无数字）。
	modelRe = regexp.MustCompile(`\b[A-Za-z]+[A-Za-z0-9._-]*\d[A-Za-z0-9._-]*\b`)

	modelAliasRes = buildModelAliasRes()

	// measureRe：数量（含单位）之后紧跟的量词。只在**已匹配数量的位置之后**尝试，
	// 不做全局扫描——量词单字（如「个」「页」）在正文里到处都是，全局扫会制造噪声实体。
	measureRe        = buildMeasureRe()
	measureCanonical = buildMeasureCanonical()

	// 计数后缀与序数前缀来自规则表（rules.json 的 counts），不在这里写死：
	// 不同语言/领域对"概括计数"的表达不同（「4 大类」vs「4 key areas」），
	// 换一套配置即可，不必改判定逻辑。
	countSuffixes    = sortedByLenDesc(counts.Suffixes)
	ordinalPrefixes  = sortedByLenDesc(counts.OrdinalPrefixes)
)

// sortedByLenDesc 按长度降序排列 token，保证长词先匹配（「关键」不应被「关」抢先）。
func sortedByLenDesc(in []string) []string {
	out := make([]string, 0, len(in))
	for _, t := range in {
		if t != "" {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// foldMultiplier 把 `x8` / `×8` 折成裸数量（保留数字、去掉前缀字母）。
func foldMultiplier(s string) string {
	if !strings.ContainsAny(s, "xX×") {
		return s
	}
	return multiplierRe.ReplaceAllStringFunc(s, func(m string) string {
		if i := strings.IndexAny(m, "×"); i >= 0 {
			return " " + m[i+len("×"):]
		}
		return " " + m[1:] // 去掉前导的 x / X
	})
}

// 等价表已数据化（M15）：声明在 rules.json，由 rules.go 内嵌加载并做启动期校验
// （空组、token 落两组、仅大小写不同的重复等只能靠校验发现的表错误）。
// 本文件只**消费**视图变量 unitAliasGroups / plainUnits / modelAliasGroups / measureGroups。
// 新增等价写法：改 rules.json，不要在本文件里再造一张表——两份必然漂移。

func buildNumberUnitRe() *regexp.Regexp {
	units := allUnits()
	// 长单位必须排在前面，否则 "sec" 会被 "s" 抢先咬成 "s" + 残留 "ec"。
	sort.SliceStable(units, func(i, j int) bool { return len(units[i]) > len(units[j]) })
	esc := make([]string, 0, len(units))
	for _, u := range units {
		esc = append(esc, regexp.QuoteMeta(u))
	}
	// 单位前空白容差用 [ \t]*（不跨行）：让「12ms / 12 ms / 12 毫秒」等价。
	// 不在单位后加 \b：`%` 后面常跟中文标点，`\b` 在「%」与「，」之间不成立，
	// 会让 `%` 分支永远匹配不上；改用 ExtractEntities 里的边界后置检查代替。
	return regexp.MustCompile(`(?i)\b(` + numberPat + `)([ \t]*(?:` + strings.Join(esc, "|") + `))?`)
}

// buildMeasureRe 由量词表生成「位置 0 处的量词」正则（长词优先，避免短词咬住长词前缀）。
func buildMeasureRe() *regexp.Regexp {
	words := allMeasures()
	sort.SliceStable(words, func(i, j int) bool { return len(words[i]) > len(words[j]) })
	esc := make([]string, 0, len(words))
	for _, w := range words {
		esc = append(esc, regexp.QuoteMeta(w))
	}
	return regexp.MustCompile(`^[ \t]*(` + strings.Join(esc, "|") + `)`)
}

// buildMeasureCanonical 把量词映射到**族名**（组内首个量词）。
// 族名只用于比较与诊断，不写进提示词的实体清单（那是给模型看的，不该暴露内部取值）。
func buildMeasureCanonical() map[string]string {
	m := map[string]string{}
	for _, g := range measureGroups {
		if len(g) == 0 {
			continue
		}
		for _, w := range g {
			m[w] = g[0]
		}
	}
	return m
}

func allMeasures() []string {
	out := make([]string, 0, len(measureGroups))
	for _, g := range measureGroups {
		out = append(out, g...)
	}
	return out
}

func buildModelAliasRes() []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(modelAliasGroups))
	for _, g := range modelAliasGroups {
		esc := make([]string, 0, len(g))
		for _, t := range g {
			esc = append(esc, regexp.QuoteMeta(t))
		}
		out = append(out, regexp.MustCompile(`(?i)\b(?:`+strings.Join(esc, "|")+`)\b`))
	}
	return out
}

func allUnits() []string {
	out := make([]string, 0, 16)
	for _, g := range unitAliasGroups {
		out = append(out, g...)
	}
	return append(out, plainUnits...)
}

var unitCanonical = func() map[string]string {
	m := map[string]string{}
	for _, g := range unitAliasGroups {
		canon := strings.ToLower(g[0])
		for _, u := range g {
			m[strings.ToLower(u)] = canon
		}
	}
	for _, u := range plainUnits {
		m[strings.ToLower(u)] = strings.ToLower(u)
	}
	return m
}()

var modelAliasCanonical = func() map[string]string {
	m := map[string]string{}
	for _, g := range modelAliasGroups {
		canon := strings.ToLower(g[0])
		for _, t := range g {
			m[strings.ToLower(t)] = canon
		}
	}
	return m
}()

// Entity is a source fact that AI text must preserve unless explicitly approved.
type Entity struct {
	Text string
	Kind string // number | date | model
	// Measure 是数量后的量词**族名**（仅 number 且原文/目标写了量词时非空）。
	// 它不参与 Text（提示词与既有调用方看到的仍是 number:3），只在比较时决定
	// 「3 个」与「3 条」兼容、「3 页」与「3 个项目」冲突。
	Measure string
	// Form 是数字的**形态**（仅 number 有意义）：实测事实 / 概括计数 / 序号。
	// 它只影响「目标里多出来的数字」怎么判，不参与 Text 与配对（见 counts.go）。
	Form string
	// Surface 是实体在原文里的**写法**（当前只有中文数词会填，如「三个」）。
	// 只给诊断用：提示要截取用户真正看到的字符，否则「新增数字 3」会去正文里找
	// 一个毫不相干的 "3"，指向错误的位置。不参与比较，也不进提示词。
	Surface string
}

// 数字形态。判定在抽取阶段完成（formOf），策略在规则表里（rules.json 的 counts）。
//
// 分成三种而不是"事实/非事实"两种，是因为序号与计数的处置不同：原文没有的
// 「第 2 步」是凭空造结构（用户已裁定不豁免），而「4 类场景」是对页面已列条目的
// 概括（2026-11-02 slide-262 因它整页回退原文）。混成一种就只能二选一。
const (
	FormFact    = "fact"    // 带单位或裸数字：实测事实，改动即违规
	FormCount   = "count"   // 紧跟量词/计数后缀：概括计数
	FormOrdinal = "ordinal" // 紧跟序数前缀（第 2 步）：序号
)

// Report is the preservation result for source-vs-target text.
type Report struct {
	Source   []Entity
	Missing  []Entity
	Inserted []Entity
	// Warnings 是按策略**放行但值得核对**的插入（当前仅概括计数）。它不参与 OK()：
	// 放行就是放行，但调用方应把提示带给用户——"无声放过"是这套门禁最不该做的事。
	Warnings []Entity
}

func (r Report) OK() bool { return len(r.Missing) == 0 && len(r.Inserted) == 0 }

// HasWarnings 报告是否存在"放行但需提示"的项。
func (r Report) HasWarnings() bool { return len(r.Warnings) > 0 }

// ExtractEntities returns normalized numeric/date/model entities in first-seen order.
// Text here is the *canonical* form of each entity, so two spellings of the same fact
// collapse to one entity (see the normalization notes at the top of this file).
func ExtractEntities(text string) []Entity {
	return extractEntities(text, chineseNumeralsOn)
}

// extractEntities 是带开关的实现：parseChinese 由规则表（chineseNumerals.enabled）决定，
// 显式传参是为了让测试能直接验证"关掉之后回到旧行为"，而不必改全局状态。
func extractEntities(text string, parseChinese bool) []Entity {
	seen := map[string]bool{}
	out := []Entity{}
	add := func(e Entity) {
		if e.Text == "" {
			return
		}
		key := e.Kind + ":" + e.Text + "@" + e.Measure
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, e)
	}

	// 全角 → 半角先做，使 １２ 与 12、Ａ１００ 与 A100 同键。
	rest := normalizeWidth(text)

	// 计数前缀 x8 / ×8 先折成裸数量（见 foldMultiplier）。
	rest = foldMultiplier(rest)

	// ① 日期：先登记，再把区间挖成等长空白，免得内部数字参与后面的数量抽取。
	rest = dateFullRe.ReplaceAllStringFunc(rest, func(m string) string {
		add(Entity{Text: canonicalDate(m), Kind: "date", Form: FormFact})
		return maskOf(m)
	})
	rest = dateYmRe.ReplaceAllStringFunc(rest, func(m string) string {
		add(Entity{Text: canonicalDate(m), Kind: "date", Form: FormFact})
		return maskOf(m)
	})

	// ② 数量 + 单位（+ 可选量词）。
	for _, m := range numberUnitRe.FindAllStringSubmatchIndex(rest, -1) {
		num := rest[m[2]:m[3]]
		// numEnd / matchEnd 必须分开：边界检查要看**数量结束处**的字符。
		// 用含单位的 matchEnd 会把 "12 msec" 误判成「字母紧跟数字」而整条丢掉。
		numEnd, matchEnd := m[3], m[1]
		unit := ""
		if m[4] >= 0 {
			unit = canonicalUnit(rest[m[4]:m[5]])
		}
		if unit != "" && endsWithASCIIAlnum(unit) && matchEnd < len(rest) && isASCIIAlnum(rest[matchEnd]) {
			// 单位被更长的单词截断（"12 msec" 里的 ms）→ 退化为裸数量
			unit, matchEnd = "", numEnd
		}
		if unit == "" && matchEnd < len(rest) && isASCIILetter(rest[matchEnd]) {
			// "5g" / "3d" / "4K"：字母紧跟数字，不是「数量+单位」，也不是型号
			// （modelRe 要求字母打头）。保持既有行为：不作为实体。
			continue
		}
		measure := measureAfter(rest, matchEnd)
		add(Entity{
			Text:    canonicalNumber(num) + unit,
			Kind:    "number",
			Measure: measure,
			Form:    numberForm(rest, m[0], matchEnd, unit != "", measure),
		})
	}

	// ③ 型号（字母打头且含数字）。
	for _, m := range modelRe.FindAllString(rest, -1) {
		add(Entity{Text: canonicalModel(m), Kind: "model", Form: FormFact})
	}

	// ④ 已登记的等效写法：目标里写全称（Kubernetes）也必须算同一个型号。
	for i, re := range modelAliasRes {
		if re.MatchString(rest) {
			add(Entity{Text: strings.ToLower(modelAliasGroups[i][0]), Kind: "model", Form: FormFact})
		}
	}

	// ⑤ 中文数词的**计数形态**（「三个」= 3 个）。放在最后：它只认汉字数词，
	// 与前面基于阿拉伯数字的抽取互不影响。词表与开关都在规则表里，判定见 chinese.go。
	if parseChinese {
		for _, m := range chineseNumeralRe.FindAllStringSubmatchIndex(rest, -1) {
			e, ok := chineseNumeralEntity(rest[m[2]:m[3]], rest[m[4]:m[5]], ordinalPrefix(rest, m[0]))
			if !ok {
				continue
			}
			e.Surface = rest[m[0]:m[1]]
			add(e)
		}
	}
	return out
}

// measureAfter 返回 pos 位置起的量词族名（无则空串）。只在数量匹配的紧邻位置调用。
func measureAfter(s string, pos int) string {
	if pos >= len(s) {
		return ""
	}
	sub := measureRe.FindStringSubmatch(s[pos:])
	if sub == nil {
		return ""
	}
	return measureCanonical[sub[1]]
}

// CheckPreserved ensures the target did not drop source entities and did not add new ones.
//
// 比较按「同 Kind + 同 Text + 量词兼容」配对（见 measuresCompatible），而不是纯集合查表：
// 集合查表无法表达"量词缺省即兼容、量词异族即冲突"这条规则。
func CheckPreserved(source, target string) Report {
	return CheckPreservedWithPolicy(source, target, counts)
}

// CheckPreservedWithPolicy 用**显式策略**比较，供规则试验与按租户覆盖使用。
//
// 默认策略只有一份（内嵌规则表）；这里把策略参数化，是为了让"换一套严格程度"不必
// 改判定逻辑，也让测试能直接构造策略而不依赖全局状态。
func CheckPreservedWithPolicy(source, target string, policy Counts) Report {
	src := ExtractEntities(source)
	dst := ExtractEntities(target)
	missing := make([]Entity, 0)
	inserted := make([]Entity, 0)
	warnings := make([]Entity, 0)
	for _, e := range src {
		// 素材有、目标没有 → 一律违规。这一侧不存在"策略放行"：事实被删是失真，
		// 与"多算了一个条目数"不是一回事。
		if !hasCompatible(dst, e) {
			missing = append(missing, e)
		}
	}
	for _, e := range dst {
		if hasCompatible(src, e) {
			continue
		}
		if toleratedInsertion(e, policy) {
			if insertionVisible(e, policy) {
				warnings = append(warnings, e)
			}
			continue
		}
		inserted = append(inserted, e)
	}
	return Report{Source: src, Missing: missing, Inserted: inserted, Warnings: warnings}
}

// hasCompatible 在 hay 里找是否存在与 needle 同 Kind、同 Text 且量词兼容的实体。
// 不做"已匹配"标记：目标里只写一次裸数字时，允许它同时满足源里「3 个」与「3 页」
// 两条记录——这是**有意偏宽容**的一侧（宁可少判违规，也不因成稿的合并表述误杀）。
func hasCompatible(hay []Entity, needle Entity) bool {
	for _, e := range hay {
		if e.Kind != needle.Kind || e.Text != needle.Text {
			continue
		}
		if measuresCompatible(needle.Measure, e.Measure) {
			return true
		}
	}
	return false
}

// measuresCompatible：任一侧未写量词视为兼容（成稿常省略/改换说法，与"事实被改"不同）；
// 两侧都写了量词时，必须同族（族名相同）。
func measuresCompatible(a, b string) bool {
	return a == "" || b == "" || a == b
}

// FormatEntities returns stable strings for prompts and tests.
//
// 量词**不进**这个字符串（族名是内部取值，不该出现在给模型的指令里）——所以同一数量
// 可能对应两条实体（如「3 个」与「3 页」），此处按字符串去重，避免指令里出现
// "number:3, number:3" 这种让模型困惑的重复项。
func FormatEntities(in []Entity) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, e := range in {
		s := e.Kind + ":" + e.Text
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// FormatEntitiesHuman 把实体渲染成**模型能读懂的提示词文本**（「数字 3」「64GB」「型号 A100」）。
//
// 与 FormatEntities 的分工必须严格：FormatEntities 输出内部键（number:3），只用于日志、
// 测试与诊断；**任何进入提示词的文本都必须走本函数**。2026-11-02 的事故就是 guard
// 指令把 number:3 原样发给模型，模型把它当成要写入正文的字面量，产出了
// 「这就是number:1」「专业编辑8能力」「本系统为版本11。」这类脏内容，而数字集合
// 恰好对得上，于是被记成 success——比失败更糟。
func FormatEntitiesHuman(in []Entity) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, e := range in {
		s := humanEntity(e)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// humanEntity 把单个实体转成自然语言片段。
func humanEntity(e Entity) string {
	switch e.Kind {
	case "number":
		return humanNumber(e.Text)
	case "date":
		return "日期 " + e.Text
	case "model":
		return "型号 " + e.Text
	default:
		return e.Text
	}
}

// humanUnit 是校验器内部单位 → 提示词写法的映射。只影响展示，不参与比较：
// 比较永远走 canonicalUnit，两者不得共用一张表的两份拷贝（规则表在 rules.json）。
var humanUnit = map[string]string{
	"%": "%", "x": "倍", "s": "秒", "ms": "毫秒", "min": "分钟", "h": "小时",
	"hz": "Hz", "khz": "kHz", "mhz": "MHz", "ghz": "GHz",
	"bps": "bps", "usd": "USD", "rmb": "RMB", "cny": "CNY",
	"tb": "TB", "gb": "GB", "mb": "MB", "kb": "KB", "b": "B",
	"kw": "kW", "mw": "MW", "w": "W", "v": "V", "a": "A",
	"元": "元", "万元": "万元", "亿元": "亿元",
}

// humanNumber 拆出「数量 + 单位」并渲染：64gb → 64GB、12ms → 12毫秒、9 → 数字 9。
// 单位可能不只一个字符，故按长度降序匹配，避免 gb 被 b 抢先截断。
func humanNumber(text string) string {
	units := make([]string, 0, len(humanUnit))
	for u := range humanUnit {
		units = append(units, u)
	}
	sort.Slice(units, func(i, j int) bool { return len(units[i]) > len(units[j]) })
	for _, u := range units {
		if len(text) <= len(u) || !strings.HasSuffix(text, u) {
			continue
		}
		num := text[:len(text)-len(u)]
		if num == "" || strings.ContainsAny(num, "abcdefghijklmnopqrstuvwxyz") {
			continue
		}
		return num + humanUnit[u]
	}
	return "数字 " + text
}

// FormatEquivalenceHint 返回给提示词用的「等价写法」说明。**与校验器共用 rules.json**：
// 指令里说「可以这样写」，校验器就必须接受；否则会出现「模型照着指令写、仍被判违规」
// 的假失败——那不是模型的问题，是指令与校验器各写了一份标准（M1/M3 的教训）。
//
// 序号（第 N 步 / N 个要点）与概括计数的处置由 rules.json 的 counts 决定（见 counts.go）：
// 默认序号仍判违规、概括计数放行并提示。等价写法说明本身不涉及这条策略，
// 它只讲"同一个事实可以怎么写"，不讲"能不能多写一个数"。
func FormatEquivalenceHint() string {
	units := make([]string, 0, len(unitAliasGroups))
	for _, g := range unitAliasGroups {
		if len(g) < 2 {
			continue
		}
		units = append(units, strings.Join(g, "/"))
	}
	sort.Strings(units)
	out := "同一事实的以下写法视为等价，可用任意一种：数字的千分位与小数尾随零可省略；" +
		"日期可写 2026-09-13 或 2026 年 9 月 13 日；单位 " + strings.Join(units, "、") + "。"
	if aliases := aliasHints(); len(aliases) > 0 {
		out += "以下写法视为同一型号：" + strings.Join(aliases, "、") + "。"
	}
	return out
}

// FormatMeasureHint 返回量词约束说明，与校验器共用同一张量词族表。
//
// 为什么必须由校验器生成而不是在提示词里另写一句：量词已参与判违规（M16），
// 若指令不说，模型把「3 页」改写成「3 个项目」就会整页回退原文，而用户不知道
// 是哪条规则触发的——正是 M1/M3「指令与校验器各写一份」的老问题。
func FormatMeasureHint() string {
	families := make([]string, 0, len(measureGroups))
	for _, g := range measureGroups {
		if len(g) == 0 {
			continue
		}
		families = append(families, strings.Join(g, "/"))
	}
	if len(families) == 0 {
		return ""
	}
	return "数字后的量词不要替换成别的量词（同组可互换：" + strings.Join(families, "，") +
		"；不同组的量词含义不同，例如「页」与「个」不可互换）。"
}

func aliasHints() []string {
	out := make([]string, 0, len(modelAliasGroups))
	for _, g := range modelAliasGroups {
		if len(g) < 2 {
			continue
		}
		out = append(out, strings.Join(g, "/"))
	}
	return out
}

// canonicalNumber 归一整数的千分位与前导零、小数尾随零：1,000→1000、09→9、3.0→3。
// 尾随零与千分位是纯排版差异，语义相同；归一后仍要求数量本身一致，
// 5 与 6 依然不同（不会把「数字被改」放过去）。
func canonicalNumber(raw string) string {
	s := strings.TrimSpace(raw)
	if strings.Contains(s, ",") {
		var b strings.Builder
		b.Grow(len(s))
		for i := 0; i < len(s); i++ {
			// 只有夹在两个数字之间的逗号才是千分位分隔符。Go 的 RE2 无后顾断言，手工扫描。
			if s[i] == ',' && i > 0 && i+1 < len(s) && isASCIIDigit(s[i-1]) && isASCIIDigit(s[i+1]) {
				continue
			}
			b.WriteByte(s[i])
		}
		s = b.String()
	}
	intPart, frac, hasFrac := strings.Cut(s, ".")
	if !hasFrac {
		return trimLeadingZeros(intPart)
	}
	frac = strings.TrimRight(frac, "0")
	if frac == "" {
		return trimLeadingZeros(intPart)
	}
	return trimLeadingZeros(intPart) + "." + frac
}

func trimLeadingZeros(s string) string {
	i := 0
	for i < len(s)-1 && s[i] == '0' {
		i++
	}
	return s[i:]
}

func canonicalUnit(raw string) string {
	u := strings.ToLower(strings.TrimSpace(raw))
	if c, ok := unitCanonical[u]; ok {
		return c
	}
	return u // 未登记的单位退化为原样小写
}

// canonicalDate 把 2026-09-13 / 2026年9月13日 / 2026 年 9 月 13 日 / 2026/9/13 归一到 2026-9-13。
func canonicalDate(raw string) string {
	nums := dateNumRe.FindAllString(raw, -1)
	out := make([]string, 0, len(nums))
	for _, n := range nums {
		out = append(out, trimLeadingZeros(n))
	}
	return strings.Join(out, "-")
}

func canonicalModel(raw string) string {
	t := strings.ToLower(trimToken(raw))
	if c, ok := modelAliasCanonical[t]; ok {
		return c
	}
	return t
}

func trimToken(raw string) string {
	v := strings.TrimSpace(raw)
	v = strings.Trim(v, "，。；：、,.!?:;()（）[]【】\"'")
	return v
}

// normalizeWidth 把全角数字/字母/小数点/百分号折成半角：１２ 与 12、Ａ１００ 与 A100 同键。
// 只折这些——中文标点保持原样（归一化不应改变断句）。
func normalizeWidth(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return r >= 0xFF01 && r <= 0xFF5E }) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 0xFF10 && r <= 0xFF19: // ０-９
			b.WriteRune(r - 0xFF10 + '0')
		case r >= 0xFF21 && r <= 0xFF3A: // Ａ-Ｚ
			b.WriteRune(r - 0xFF21 + 'A')
		case r >= 0xFF41 && r <= 0xFF5A: // ａ-ｚ
			b.WriteRune(r - 0xFF41 + 'a')
		case r == 0xFF0E: // ．
			b.WriteRune('.')
		case r == 0xFF05: // ％
			b.WriteRune('%')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// maskOf 用等长空白替换已识别的片段，使其内部字符不再参与后续匹配。
func maskOf(m string) string {
	return strings.Repeat(" ", utf8.RuneCountInString(m))
}

func isASCIIDigit(c byte) bool { return c >= '0' && c <= '9' }

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isASCIIAlnum(c byte) bool { return isASCIIDigit(c) || isASCIILetter(c) }

func endsWithASCIIAlnum(s string) bool {
	if s == "" {
		return false
	}
	return isASCIIAlnum(s[len(s)-1])
}
