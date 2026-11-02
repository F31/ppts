// 规则表的数据化与自检（M15，2026-09-22）。
//
// 此前等价表是散在 entities.go 里的 Go 源码常量：加一条等价写法要改 Go 代码、重新编译部署，
// 而且**表本身的错误没有任何检查**——例如同一个 token 落在两个组里，归一化结果会静默地
// 取决于声明顺序（`unitCanonical` 后写覆盖先写），编译期与运行期都不会报错。
//
// 现在表搬到 rules.json，本文件负责：
//  1. 内嵌加载（go:embed，无 IO、无 DB 依赖——安全门禁必须确定性，不能因数据库不可用而变行为）；
//  2. **启动期校验**（ValidateRules）：表结构错误在进程起飞前就炸掉，而不是等某个租户的
//     页面被静默放行；
//  3. 暴露 RulesVersion 供报告/基线留痕（"这份判定是在哪版规则下得到的"）。
//
// 为什么不放进数据库/租户配置：等价表是安全门禁的底线，若可被配置改动，"同一份文本在两处
// 结论不同"的缺陷将不可复现。需要租户可配的是**领域型号别名**那一层，且必须只增不减——
// 那属于后续里程碑，不在本文件范围内。
package validation

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed rules.json
var rulesJSON []byte

// Rules 是等价表的声明形式。
type Rules struct {
	Note string `json:"_note,omitempty"`
	// Version 供报告与基线留痕；改表必须同时改它（有测试断言非空）。
	Version string `json:"version"`
	// UnitAliasGroups：同一物理量的不同写法，组内首个为规范写法。
	UnitAliasGroups [][]string `json:"unitAliasGroups"`
	// PlainUnits：只有一种写法的单位（中文技术文案里直接沿用英文缩写）。
	PlainUnits []string `json:"plainUnits"`
	// ModelAliasGroups：缩写与其通用全称的语言级同义，组内首个为规范写法。
	ModelAliasGroups [][]string `json:"modelAliasGroups"`
	// MeasureGroups：量词按**语义家族**分组（M16）。同族可互换（个/项/条/款），
	// 异族不可（页 ≠ 个）——"3 页"与"3 个项目"是两件事，而"3 个"与"3 条"是同一件事。
	// 只登记确定可互换的；吃不准的量词**单独成组**（跨族即判违规，取保守侧）。
	MeasureGroups [][]string `json:"measureGroups"`
	// Counts 决定「目标里多出来的数字」怎么判（见 counts.go）。缺省即全部 block，
	// 与 M11 以来"原文没有的数字一律拦"的行为一致——不写这段配置不会悄悄放宽门禁。
	Counts Counts `json:"counts"`
	// ChineseNumerals 决定中文数词（「三个」「两个方面」）是否参与比较（见 chinese.go）。
	// 缺省启用：模型把「3 个」写成「三个」是既成事实，不解析就是误杀。设 false 可整体
	// 退回"只认阿拉伯数字"的旧行为，作为线上回滚开关（不必改代码重发）。
	ChineseNumerals ChineseNumerals `json:"chineseNumerals"`
}

// ChineseNumerals 是中文数词解析的开关与词表微调。
type ChineseNumerals struct {
	// Enabled 缺省 true。
	Enabled *bool `json:"enabled,omitempty"`
	// ExcludeTails 从"允许跟在数词后的词"里剔除若干项，只影响中文数词。
	// 默认含「步」：「进一步」「一步步」是固定说法，解析成「1 步」会让正常改写被判缺失。
	ExcludeTails []string `json:"excludeTails,omitempty"`
}

// enabled 报告是否解析中文数词（未声明即启用）。
func (c ChineseNumerals) enabled() bool { return c.Enabled == nil || *c.Enabled }

// 插入数字的三种处置。block 判违规；warn/allow 都放行，区别只在是否在步骤上提示。
//
// 为什么不用布尔：warn 与 allow 的差别是**可见性**而不是门禁强度。把"放过一个模型
// 自己数出来的条目数"静默处理，等于让用户失去核对机会；这条链路上已经吃过一次
// "假成功"的亏（2026-11-02），所以默认值取 warn。
const (
	ModeBlock = "block"
	ModeWarn  = "warn"
	ModeAllow = "allow"
)

// Counts 是数字形态策略：把「插入的数字」按**形态**分流，而不是一刀切。
//
// 形态由抽取阶段判定（见 entities.go 的 formOf）：带单位 = 实测事实（永远严格）；
// 紧跟序数前缀 = 序号；紧跟量词/计数后缀 = 概括计数；其余 = 裸数字（按事实处理）。
//
// 之所以要分流：实测事实（64GB→96GB）改一个数字就是错，而「4 类场景」是模型对
// 页面已列条目的概括——两者混在一条规则里，只能二选一：要么放过真实的数字篡改，
// 要么把正常的概括判成编造（2026-11-02 slide-262 即此）。
type Counts struct {
	// InsertedCounts / InsertedOrdinals 取 ModeBlock / ModeWarn / ModeAllow。
	InsertedCounts   string `json:"insertedCounts"`
	InsertedOrdinals string `json:"insertedOrdinals"`
	// MaxValue 是计数豁免的上限：超过它的"计数"不再当作概括（编造「120 个城市」必须拦）。
	// <=0 表示不限制。
	MaxValue float64 `json:"maxValue"`
	// OrdinalPrefixes 是数字前的序数标记（"第 2 步"）。比较时忽略中间空白。
	OrdinalPrefixes []string `json:"ordinalPrefixes"`
	// Suffixes 是量词之外的计数后缀（"4 大场景"里的「大」、「3 个维度」已由量词覆盖）。
	// 只登记**不会与内容冲突**的词：像「点」会与时间（3 点）混淆，不登记。
	Suffixes []string `json:"suffixes"`
}

// defaultRules 是进程内唯一的一份规则表；加载或校验失败直接 panic——
// 内嵌数据损坏意味着二进制作废，静默降级会让安全门禁在无人察觉的情况下失效。
var defaultRules = mustLoadRules()

// 三个视图变量在 rules.go 里赋值（表本身在 rules.json）。等价关系只声明一次，
// 提示词侧通过 FormatEquivalenceHint 取同一张表，避免"指令说可以这样写、校验器却拒绝"。
var (
	unitAliasGroups  = defaultRules.UnitAliasGroups
	plainUnits       = defaultRules.PlainUnits
	modelAliasGroups = defaultRules.ModelAliasGroups
	measureGroups    = defaultRules.MeasureGroups
	counts           = defaultRules.Counts
	// chineseNumerals 与 chineseNumeralsOn 分开：前者给词表用（排除项），后者给抽取分支用。
	chineseNumerals   = defaultRules.ChineseNumerals
	chineseNumeralsOn = chineseNumerals.enabled()
)

// CountsPolicy 返回当前的数字形态策略（只读）。调用方据此决定是否在步骤上提示
// 「这个数字是模型自己数出来的」——策略在规则表里，判断逻辑不该在调用方各写一份。
func CountsPolicy() Counts { return counts }

// RulesVersion 返回当前规则表版本，供回放报告/基线留痕。
func RulesVersion() string { return defaultRules.Version }

// DefaultRules 返回内嵌的规则表（测试与工具用；返回值只读）。
func DefaultRules() Rules { return defaultRules }

// ParseRules 解析规则表声明；结构错误在 ValidateRules 里报。
func ParseRules(data []byte) (Rules, error) {
	var r Rules
	if err := json.Unmarshal(data, &r); err != nil {
		return Rules{}, fmt.Errorf("解析规则表: %w", err)
	}
	return r, nil
}

func mustLoadRules() Rules {
	r, err := ParseRules(rulesJSON)
	if err != nil {
		panic("validation: 内嵌规则表损坏: " + err.Error())
	}
	if problems := ValidateRules(r); len(problems) > 0 {
		panic("validation: 内嵌规则表非法: " + strings.Join(problems, "; "))
	}
	return r
}

// ValidateRules 检查规则表的**结构性错误**，返回人类可读的问题列表（空 = 合法）。
//
// 覆盖的都是"编译期看不见、运行期不报错、只表现为某个实体被静默放行/误杀"的错误：
//   - 空 token / token 含空白（空白在正则里会被 QuoteMeta 后仍匹配不上，归一化悄悄失效）
//   - 等价组只有 1 个写法（不是等价，是打字错误）
//   - 同一 token 出现在两个等价组，或同时出现在等价组与 plainUnits
//     → 归一化结果取决于声明顺序（后写覆盖先写），换个顺序行为就变，却没有报错
//   - 单位集合里存在仅大小写不同的重复（大小写被折叠后同样会互相覆盖）
func ValidateRules(r Rules) []string {
	var problems []string
	if strings.TrimSpace(r.Version) == "" {
		problems = append(problems, "version 不能为空（改表必须留痕）")
	}
	if len(r.UnitAliasGroups) == 0 && len(r.PlainUnits) == 0 &&
		len(r.ModelAliasGroups) == 0 && len(r.MeasureGroups) == 0 {
		problems = append(problems, "四张子表全空：门禁会退化成几乎不校验")
	}

	check := func(ns string, owner map[string]string, where, token string) {
		t := strings.ToLower(strings.TrimSpace(token))
		switch {
		case t == "":
			problems = append(problems, where+" 含空 token")
			return
		case strings.ContainsAny(token, " \t\r\n"):
			problems = append(problems, where+" 的 token 含空白："+fmt.Sprintf("%q", token))
			return
		}
		if prev, dup := owner[t]; dup {
			problems = append(problems, fmt.Sprintf(
				"%s 里 token %q 同时出现在 %s 与 %s：归一化结果会取决于声明顺序（后写覆盖先写）",
				ns, t, prev, where))
			return
		}
		owner[t] = where
	}

	units := map[string]string{}
	for i, g := range r.UnitAliasGroups {
		where := fmt.Sprintf("unitAliasGroups[%d]", i)
		if len(g) < 2 {
			problems = append(problems, fmt.Sprintf("%s 只有 %d 个写法：等价组至少需要 2 个", where, len(g)))
		}
		for _, t := range g {
			check("单位", units, where, t)
		}
	}
	for _, t := range r.PlainUnits {
		check("单位", units, "plainUnits", t)
	}

	models := map[string]string{}
	for i, g := range r.ModelAliasGroups {
		where := fmt.Sprintf("modelAliasGroups[%d]", i)
		if len(g) < 2 {
			problems = append(problems, fmt.Sprintf("%s 只有 %d 个写法：等价组至少需要 2 个", where, len(g)))
		}
		for _, t := range g {
			check("型号", models, where, t)
		}
	}

	// 量词组与上面两组规则不同：**允许只有一个量词**——吃不准能否互换的量词应当单独成组
	// （跨族即判违规，取保守侧），而不是为了凑够两个而硬拉一个近似词进组。
	measures := map[string]string{}
	for i, g := range r.MeasureGroups {
		where := fmt.Sprintf("measureGroups[%d]", i)
		if len(g) == 0 {
			problems = append(problems, where+" 为空：空组没有意义，要么删掉要么填量词")
			continue
		}
		for _, t := range g {
			tl := strings.ToLower(strings.TrimSpace(t))
			if tl != "" {
				if prev, isUnit := units[tl]; isUnit {
					problems = append(problems, fmt.Sprintf(
						"量词 %q 同时登记为单位（%s）：抽取时会被单位规则先吃掉，量词判定失效", t, prev))
					continue
				}
			}
			check("量词", measures, where, t)
		}
	}
	problems = append(problems, validateCounts(r.Counts)...)
	problems = append(problems, validateChineseNumerals(r.ChineseNumerals)...)
	return problems
}

// validateChineseNumerals 检查中文数词配置的结构性错误。
//
// 排除项写错的后果很隐蔽：写成「步 」（带空格）或重复登记不会报错，只会让排除
// **静默失效**——「进一步」重新被解析成「1 步」，成稿开始莫名降级，而配置看起来是
// "已经处理过"的。这与 counts 里的后缀表是同一类错误，沿用同一套检查。
func validateChineseNumerals(c ChineseNumerals) []string {
	var problems []string
	seen := map[string]bool{}
	for _, t := range c.ExcludeTails {
		switch {
		case strings.TrimSpace(t) == "":
			problems = append(problems, "chineseNumerals.excludeTails 含空 token")
		case strings.ContainsAny(t, " \t\r\n"):
			problems = append(problems, fmt.Sprintf(
				"chineseNumerals.excludeTails 的 token 含空白：%q（不会匹配成功，等于没排除）", t))
		case seen[t]:
			problems = append(problems, fmt.Sprintf("chineseNumerals.excludeTails 重复 token：%q", t))
		}
		seen[t] = true
	}
	return problems
}

// validateCounts 检查数字形态策略的**结构性错误**。
//
// 这里的错误同样属于"编译期看不见、运行期不报错、只表现为某类数字被静默放过"：
// 模式写错（"warm"）会让门禁按默认值静默生效，maxValue 漏写会让任意大的编造计数
// 都被当作概括，后缀表空则 count 形态永远不会被识别——三种都不会报错。
func validateCounts(c Counts) []string {
	var problems []string
	checkMode := func(field, v string) {
		switch v {
		case "", ModeBlock, ModeWarn, ModeAllow:
			return
		default:
			problems = append(problems, fmt.Sprintf(
				"%s=%q 非法：只能是 %s / %s / %s（留空视为 %s）",
				field, v, ModeBlock, ModeWarn, ModeAllow, ModeBlock))
		}
	}
	checkMode("counts.insertedCounts", c.InsertedCounts)
	checkMode("counts.insertedOrdinals", c.InsertedOrdinals)

	tolerant := func(m string) bool { return m == ModeWarn || m == ModeAllow }
	if (tolerant(c.InsertedCounts) || tolerant(c.InsertedOrdinals)) && c.MaxValue <= 0 {
		problems = append(problems,
			"counts 启用了豁免但 maxValue<=0：任意大的编造计数都会被当作概括，必须给定上限")
	}

	tokens := func(where string, list []string) {
		seen := map[string]bool{}
		for _, t := range list {
			switch {
			case strings.TrimSpace(t) == "":
				problems = append(problems, where+" 含空 token")
			case strings.ContainsAny(t, " \t\r\n"):
				problems = append(problems, where+" 的 token 含空白："+fmt.Sprintf("%q", t))
			case seen[t]:
				problems = append(problems, where+" 重复 token："+fmt.Sprintf("%q", t))
			}
			seen[t] = true
		}
	}
	tokens("counts.ordinalPrefixes", c.OrdinalPrefixes)
	tokens("counts.suffixes", c.Suffixes)
	return problems
}
