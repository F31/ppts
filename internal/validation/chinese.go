package validation

// 中文数词的**计数形态**解析（2026-11-02）。
//
// 缺口（语料里挂了两周的 auth-chinese-numeral-known-gap）：页面写「共 3 个要点」，
// 模型写成「共三个要点。」，抽取器只认阿拉伯数字，于是判「缺失数字 3」→ 两轮修补
// 都不过 → 整页降级。这是**误杀**：中文数字与阿拉伯数字是同一事实的两种写法。
//
// 为什么不"见到数词就解析"：「一定」「统一」「一起」「两全」「进一步」「一方面」
// 里的数词不是数量。全局解析会凭空造出数字实体，把正常成稿判成"新增数字/缺失数字"，
// 比现在的误杀更糟（用户看到的是随机降级，且无法解释）。
//
// 因此形态在抽取期就限定死：**数词后必须紧跟量词或计数后缀**，且：
//   - 排除表（rules.json 的 chineseNumerals.excludeTails）挡掉固定说法：默认含「步」，
//     「进一步」「一步步」不是「1 步」；
//   - 值为 1 时只认**量词**、不认计数后缀：「一方面」「一部分」「一维度」是固定说法，
//     而「一个」「一项」是真计数；
//   - 数词本身必须是规范写法（小节内单位严格递减、不允许两个非零数字相邻），
//     「两三」「十百」这类模糊/错写解析失败 → 退回"不解析"，宁可不校验也不造假实体。
//
// 解析结果按**阿拉伯数字**登记（「三个」→ number 3），并标 FormCount：这样它与
// 「3 个」同键可比，插多/插少仍按计数策略处置（见 counts.go），不会因为换了个写法
// 就绕开「数字被改」的拦截。

import (
	"regexp"
	"strconv"
	"strings"
)

// chineseNumeralRe 由词表生成：数词串 + （量词 | 计数后缀），长词优先。
// 排除表**不**在这里生效：排除与否要看是否序数语境（「第三步」要留、「进一步」要丢），
// 那是候选级的判断，见 chineseNumeralEntity。
var chineseNumeralRe = buildChineseNumeralRe()

// chineseNumeralTails 是允许跟在中文数词后的词（量词 + 计数后缀）。
func chineseNumeralTails() []string {
	out := make([]string, 0, len(measureGroups)+len(counts.Suffixes))
	for _, g := range measureGroups {
		out = append(out, g...)
	}
	return append(out, counts.Suffixes...)
}

func buildChineseNumeralRe() *regexp.Regexp {
	tails := sortedByLenDesc(chineseNumeralTails())
	if len(tails) == 0 {
		// 没有可跟的词时给一个永不匹配的式子（\b\B 在任何输入上都不成立），
		// 而不是留一个能匹配空串的正则。
		return regexp.MustCompile(`\b\B`)
	}
	esc := make([]string, 0, len(tails))
	for _, t := range tails {
		esc = append(esc, regexp.QuoteMeta(t))
	}
	return regexp.MustCompile(`([零一二三四五六七八九十百千万亿两]+)(` + strings.Join(esc, "|") + `)`)
}

var (
	cnDigits   = map[rune]int{'零': 0, '一': 1, '二': 2, '两': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	cnUnits    = map[rune]int{'十': 10, '百': 100, '千': 1000}
	cnSections = map[rune]int{'万': 10000, '亿': 100000000}
)

// parseChineseInteger 把「三」「十二」「二十」「一百零三」「两千」「十二万」解析成整数。
//
// 只接受规范写法：小节内单位必须严格递减（百→十可以，十→百不行），且不允许两个
// 非零数字相邻（「两三」「三五」是模糊说法，不是确定数量）。解析失败一律返回 false，
// 调用方据此跳过——宁可少一条实体，也不把模糊说法固化成一个数字。
func parseChineseInteger(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	total, section, num := 0, 0, 0
	lastUnit := 1 << 30
	started := false
	for _, r := range s {
		if d, ok := cnDigits[r]; ok {
			if d == 0 {
				continue // 「零」只是占位，不参与计算
			}
			if num != 0 {
				return 0, false // 「两三」：两个非零数字相邻
			}
			num = d
			started = true
			continue
		}
		if unit, ok := cnUnits[r]; ok {
			if num == 0 {
				if started {
					return 0, false // 「三零百」这类不该出现
				}
				num = 1 // 「十二」「十」：前导的一省略
			}
			if unit >= lastUnit {
				return 0, false // 「十百」：单位必须严格递减
			}
			lastUnit = unit
			section += num * unit
			num = 0
			started = true
			continue
		}
		if mul, ok := cnSections[r]; ok {
			section += num
			if section == 0 {
				return 0, false
			}
			total = (total + section) * mul
			section, num, started, lastUnit = 0, 0, false, 1<<30
			continue
		}
		return 0, false
	}
	total += section + num
	if total <= 0 {
		return 0, false
	}
	return total, true
}

// chineseNumeralEntity 把一处「数词+量词/后缀」转成实体；不合格返回 ok=false。
//
// ordinal 表示数词前紧邻序数标记（「第三个」「第三步」「第一章」）。它放宽两条限制：
//   - 排除表（如「步」）在序数语境下不生效：「第三步」是序号，必须与「第 3 步」可比；
//   - 值为 1 时允许跟计数后缀：「第一章」「第一部分」是序号，而「一方面」不是。
func chineseNumeralEntity(numeral, tail string, ordinal bool) (Entity, bool) {
	if !ordinal {
		for _, t := range chineseNumerals.ExcludeTails {
			if t == tail {
				return Entity{}, false
			}
		}
	}
	v, ok := parseChineseInteger(numeral)
	if !ok {
		return Entity{}, false
	}
	measure := measureCanonical[tail]
	if v == 1 && measure == "" && !ordinal {
		// 值为 1 且后面是计数后缀：「一方面」「一部分」是固定说法，不是「1 个方面」。
		return Entity{}, false
	}
	return Entity{Text: strconv.Itoa(v), Kind: "number", Measure: measure, Form: FormCount}, true
}
